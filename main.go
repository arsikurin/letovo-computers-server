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
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/volatiletech/sqlboiler/v4/boil"

	"letovo-computers-server/models"
	"letovo-computers-server/types"
)

const (
	serverWillTopic    = "comps/server/will"
	arduinoWillTopic   = "comps/arduino/will"
	serverStreamTopic  = "comps/server/stream"
	arduinoStreamTopic = "comps/arduino/stream"
)

func main() {
	fmt.Println(`
server started
	`)

	debug := flag.Bool("debug", false, "sets log level to debug")
	flag.Parse()

	// Default level is info, unless debug flag is present
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	initLogger()

	err := godotenv.Load(".env")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load .env file")
	}

	db, err := sql.Open("postgres", fmt.Sprintf(
		"postgres://%s:%s@%s:5432/%s?sslmode=disable",
		os.Getenv("PG_USER"), os.Getenv("PG_PASSWORD"), os.Getenv("PG_HOST"), os.Getenv("PG_DBNAME"),
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
	client := initBroker()
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal().Err(token.Error()).Msg("failed to connect to broker")
	}

	quit := make(chan os.Signal, 1)
	go func() {
		if err := start(client, quit); err != nil {
			log.Error().Err(err).Msg("Shutting down the server")

			signal.Notify(quit, os.Interrupt)
		}
	}()

	signal.Notify(quit, os.Interrupt)
	<-quit
}

func initLogger() {
	zerolog.TimeFieldFormat = time.RFC3339 // TimeFormatUnix
	zerolog.TimestampFieldName = "timestamp"
	zerolog.LevelFieldName = "level"
	zerolog.MessageFieldName = "message"
	zerolog.ErrorFieldName = "error"
	zerolog.CallerFieldName = "caller"
	zerolog.CallerMarshalFunc = func(pc uintptr, file string, line int) string {
		return fmt.Sprintf("%s:%d", file, line)
	}

	runLogFile, err := os.OpenFile(
		"letovo-computers.log",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0664,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open or create log file")
	}

	log.Logger = zerolog.New(runLogFile).With().Timestamp().Caller().Logger()
}

func start(client mqtt.Client, doneChan chan os.Signal) error {
	ctx := context.Background()
	var wg sync.WaitGroup

	publish(&wg, client, serverStreamTopic, "hi from go")

	subscribe(&wg, client, arduinoStreamTopic, 2, func(ctx context.Context) func(client mqtt.Client, resp mqtt.Message) {
		return func(client mqtt.Client, resp mqtt.Message) {
			message := new(types.MQTTMessage)

			err := json.Unmarshal(resp.Payload(), message)
			if err != nil {
				log.Error().Err(err).Msg("failed to unmarshal message")
				return
			}

			log.Debug().
				Str("RFID", message.RFID).
				Str("slots", message.Slots).
				Int("status", int(message.Status)).
				Msg(message.Message)

			switch message.Status {
			case types.Placed:
				log.Info().
					Str("RFID", message.RFID).
					Str("slots", message.Slots).
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
						boil.Whitelist("taken_by", "is_taken"), boil.Infer())
					if err != nil {
						log.Error().Err(err).Msg("failed to upsert slot to db in Placed case")
					}
				}

			case types.Taken:
				log.Info().
					Str("RFID", message.RFID).
					Str("slots", message.Slots).
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
						boil.Whitelist("taken_by", "is_taken"), boil.Infer())
					if err != nil {
						log.Error().Err(err).Msg("failed to upsert slot to db in Taken case")
					}
				}

			case types.Scanned:
				log.Info().Msgf("scanned a %s tag ", message.RFID)
				user := models.User{
					ID: message.RFID,
				}

				err := user.UpsertG(ctx, true, []string{"id"},
					boil.Whitelist("login"), boil.Infer())
				if err != nil {
					log.Error().Err(err).Msg("failed to upsert user to db in Scanned case")
				}
			}
		}
	}(ctx))

	subscribe(&wg, client, arduinoWillTopic, 2, func(ctx context.Context) func(client mqtt.Client, resp mqtt.Message) {
		return func(client mqtt.Client, resp mqtt.Message) {
			log.Info().Msgf("arduino %s is offline", resp.Topic())
			log.Debug().Msgf("%s %s %t %d %t %d\n", resp.Topic(), resp.Payload(), resp.Duplicate(), resp.Qos(), resp.Retained(), resp.MessageID())
		}
	}(ctx))

	wg.Wait()

	select {
	case <-doneChan:
		client.Disconnect(250)
	}

	_, cancel := context.WithTimeout(ctx, time.Second*10) //nolint:gomnd
	defer cancel()

	return nil
}

func initBroker() mqtt.Client {
	opts := mqtt.NewClientOptions().
		AddBroker(fmt.Sprintf("tls://%s:%s", os.Getenv("MQTT_HOST"), os.Getenv("MQTT_PORT"))).
		SetClientID(os.Getenv("MQTT_CLIENT_ID")).
		SetUsername(os.Getenv("MQTT_USER")).
		SetPassword(os.Getenv("MQTT_PASS")).
		SetConnectionLostHandler(func(client mqtt.Client, err error) {
			log.Warn().Err(err).Msg("Connection lost to broker")
		}).
		SetOnConnectHandler(func(client mqtt.Client) {
			log.Info().Msg("Connected to broker")
		}).
		SetBinaryWill(
			serverWillTopic, []byte("{\"message\":\"server disconnected\"}"), 2, true,
		)

	return mqtt.NewClient(opts)
}

func subscribe(wg *sync.WaitGroup, client mqtt.Client, topic string, qos byte, callback func(client mqtt.Client, resp mqtt.Message)) {
	wg.Add(1)
	t := client.Subscribe(topic, qos, callback)

	go func() {
		defer wg.Done()

		<-t.Done()
		if t.Error() != nil {
			log.Error().Err(t.Error()).Msgf("failed to subscribe to %s", topic)
		}
	}()
}

func publish(wg *sync.WaitGroup, client mqtt.Client, topic string, payload string) {
	wg.Add(1)
	t := client.Publish(topic, 2, true, payload)

	go func() {
		defer wg.Done()

		<-t.Done()
		if t.Error() != nil {
			log.Error().Err(t.Error()).Msg("failed to publish message")
		}
	}()
}
