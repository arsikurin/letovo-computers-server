//go:generate sqlboiler psql -c sqlboiler.toml

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/volatiletech/sqlboiler/v4/boil"

	"letovo-computers-management/models"
	"letovo-computers-management/types"
)

func subscribe(wg *sync.WaitGroup, client mqtt.Client, topic string, qos byte, callback func(client mqtt.Client, resp mqtt.Message)) {
	wg.Add(1)
	t := client.Subscribe(topic, qos, callback)

	go func() {
		defer wg.Done()

		<-t.Done()
		if t.Error() != nil {
			log.Println(t.Error())
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
			log.Println(t.Error())
		}
	}()
}

const (
	serverWillTopic    = "comps/server/will"
	arduinoWillTopic   = "comps/arduino/will"
	serverStreamTopic  = "comps/server/stream"
	arduinoStreamTopic = "comps/arduino/stream"
)

func initBroker() mqtt.Client {
	opts := mqtt.NewClientOptions().
		AddBroker(fmt.Sprintf("tls://%s:%s", os.Getenv("MQTT_HOST"), os.Getenv("MQTT_PORT"))).
		SetClientID(os.Getenv("MQTT_CLIENT_ID")).
		SetUsername(os.Getenv("MQTT_USER")).
		SetPassword(os.Getenv("MQTT_PASS")).
		SetConnectionLostHandler(func(client mqtt.Client, err error) {
			log.Printf("Connection lost: %v\n", err)
		}).
		SetOnConnectHandler(func(client mqtt.Client) {
			log.Println("Connected")
		}).
		SetBinaryWill(
			serverWillTopic, []byte("{\"message\":\"server disconnected\"}"), 2, true,
		)

	return mqtt.NewClient(opts)
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
				log.Println(err)
				return
			}

			switch message.Status {
			case types.Placed:
				fmt.Printf("%s placed computer to %s\n", message.RFID, message.Slots)
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
						log.Println(err)
					}
				}

			case types.Taken:
				fmt.Printf("%s took computer from %s\n", message.RFID, message.Slots)
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
						log.Println(err)
					}
				}

			case types.Scanned:
				fmt.Println("scanned tag", message.RFID)
				user := models.User{
					ID: message.RFID,
				}

				err := user.UpsertG(ctx, true, []string{"id"},
					boil.Whitelist("login"), boil.Infer())
				if err != nil {
					log.Println(err)
				}
			}

			fmt.Println(message.Message, message.RFID, message.Slots)
		}
	}(ctx))

	subscribe(&wg, client, arduinoWillTopic, 2, func(ctx context.Context) func(client mqtt.Client, resp mqtt.Message) {
		return func(client mqtt.Client, resp mqtt.Message) {
			fmt.Printf("%s %s %t %d %t %d\n", resp.Topic(), resp.Payload(), resp.Duplicate(), resp.Qos(), resp.Retained(), resp.MessageID())
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

func main() {
	fmt.Println(`
server started
	`)
	// _ = zerolog.New(os.Stdout)

	err := godotenv.Load(".env")
	if err != nil {
		log.Fatalln("Error loading .env file")
	}

	db, err := sql.Open("postgres", fmt.Sprintf(
		"postgres://%s:%s@%s:5432/%s?sslmode=disable",
		os.Getenv("PG_USER"), os.Getenv("PG_PASSWORD"), os.Getenv("PG_HOST"), os.Getenv("PG_DBNAME"),
	))
	if err != nil {
		log.Fatalln(err)
	}

	boil.SetDB(db)
	defer func(db *sql.DB) {
		err := db.Close()
		if err != nil {
			log.Println(err)
		}
	}(db)
	client := initBroker()
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalln(token.Error())
	}

	quit := make(chan os.Signal, 1)
	go func() {
		if err := start(client, quit); err != nil {
			log.Println(err)
			log.Fatal("Shutting down the server")
		}
	}()
	signal.Notify(quit, os.Interrupt)
	<-quit
}
