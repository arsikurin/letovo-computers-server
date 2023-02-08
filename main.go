//go:generate sqlboiler psql -c sqlboiler.toml

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"gopkg.in/natefinch/lumberjack.v2"

	"letovo-computers-server/broker"
	"letovo-computers-server/models"
	"letovo-computers-server/types"
)

func init() {
	debug := flag.Bool("debug", false, "sets log level to debug")
	flag.Parse()

	// Default level is info, unless debug flag is present
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	zerolog.TimestampFieldName = "timestamp"
	zerolog.CallerMarshalFunc = func(pc uintptr, file string, line int) string {
		return fmt.Sprintf("%s:%d", file, line)
	}

	fileLogger := lumberjack.Logger{
		Filename:  "/var/log/letovo-computers/server.log",
		MaxSize:   500,
		MaxAge:    30,
		LocalTime: true,
		Compress:  true,
	}

	log.Logger = zerolog.New(zerolog.MultiLevelWriter(os.Stdout, &fileLogger)).With().Timestamp().Logger()

	// rotateChan := make(chan os.Signal, 1)
	// signal.Notify(rotateChan, syscall.SIGHUP)
	// go func() {
	// 	for {
	// 		<-rotateChan
	// 		err := fileLogger.Rotate()
	// 		if err != nil {
	// 			log.Error().Err(err).Msg("failed to rotate log file")
	// 		}
	// 	}
	// }()
}

func main() {
	log.Debug().Msg("Starting the server")

	err := godotenv.Load(".env")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load .env file")
	}

	db, err := sql.Open("postgres", fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		os.Getenv("PG_USER"), os.Getenv("PG_PASSWORD"), os.Getenv("PG_HOST"),
		os.Getenv("PG_PORT"), os.Getenv("PG_DBNAME"), os.Getenv("PG_SSLMODE"),
	))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to db")
	}

	err = db.Ping()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to ping db")
	}

	boil.SetDB(db)
	defer func(db *sql.DB) {
		err := db.Close()
		if err != nil {
			log.Error().Err(err).Msg("failed to close db")
		}
	}(db)

	client := broker.Init()
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal().Err(token.Error()).Msg("failed to connect to broker")
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	quit := make(chan bool, 1)

	go func() {
		if err := start(client, sigs); err != nil {
			log.Error().Err(err).Msg("Shutting down the server due to an error")
		}

		quit <- true
	}()

	<-quit
	log.Debug().Msg("Gracefully shut down the server")
}

func start(client mqtt.Client, sigs chan os.Signal) error {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	broker.Publish(&wg, client, os.Getenv("SERVER_STREAM_TOPIC"), "hi from go")

	broker.Subscribe(&wg, client, os.Getenv("ARDUINO_STREAM_TOPIC"), 2,
		func(ctx context.Context) func(client mqtt.Client, resp mqtt.Message) {
			return func(client mqtt.Client, resp mqtt.Message) {
				message := new(types.MQTTMessage)

				err := json.Unmarshal(resp.Payload(), message)
				if err != nil {
					log.Error().Err(err).Msg("failed to unmarshal message")
					return
				}

				switch message.Status {
				case types.Placed:
					log.Info().
						Str("RFID", message.RFID).
						Str("slots", message.Slots).
						Int("status", int(message.Status)).
						Msgf("%s placed computer to %s", message.RFID, message.Slots)

					for _, slotID := range strings.Split(message.Slots, ";") {
						if slotID == "" {
							continue
						}

						slot := models.Slot{
							ID:      slotID,
							TakenBy: message.RFID,
							IsTaken: false,
						}

						err = slot.UpsertG(ctx, true, []string{"id"},
							boil.Whitelist("taken_by", "is_taken"), boil.Infer(),
						)
						if err != nil {
							log.Error().Err(err).Msg("failed to upsert slot to db in Placed case")
						}
					}

				case types.Taken:
					log.Info().
						Str("RFID", message.RFID).
						Str("slots", message.Slots).
						Int("status", int(message.Status)).
						Msgf("%s took computer from %s", message.RFID, message.Slots)

					for _, slotID := range strings.Split(message.Slots, ";") {
						if slotID == "" {
							continue
						}

						slot := models.Slot{
							ID:      slotID,
							TakenBy: message.RFID,
							IsTaken: true,
						}

						err = slot.UpsertG(ctx, true, []string{"id"},
							boil.Whitelist("taken_by", "is_taken"), boil.Infer(),
						)
						if err != nil {
							log.Error().Err(err).Msg("failed to upsert slot to db in Taken case")
						}
					}

				case types.Scanned:
					log.Info().
						Str("RFID", message.RFID).
						Int("status", int(message.Status)).
						Msgf("scanned the %s tag ", message.RFID)

					user := models.User{
						ID: message.RFID,
					}

					err := user.UpsertG(ctx, true, []string{"id"},
						boil.Whitelist("login"), boil.Infer(),
					)
					if err != nil {
						log.Error().Err(err).Msg("failed to upsert user to db in Scanned case")
					}

				default:
					log.Warn().
						Str("RFID", message.RFID).
						Str("slots", message.Slots).
						Int("status", int(message.Status)).
						Msg(message.Message)
				}
			}
		}(ctx))

	broker.Subscribe(&wg, client, os.Getenv("ARDUINO_WILL_TOPIC"), 2,
		func(ctx context.Context) func(client mqtt.Client, resp mqtt.Message) {
			return func(client mqtt.Client, resp mqtt.Message) {
				log.Warn().Msgf("arduino %s is offline", resp.Payload())
				log.Debug().Msgf("%s %s %t %d %t %d\n", resp.Topic(), resp.Payload(), resp.Duplicate(), resp.Qos(), resp.Retained(), resp.MessageID())
			}
		}(ctx),
	)

	wg.Wait()

	log.Info().Msg("Server is ready to handle requests")

	select {
	case <-sigs:
		client.Disconnect(250)
	}

	return nil
}
