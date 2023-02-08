package broker

import (
	"fmt"
	"os"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

func Init() mqtt.Client {
	opts := mqtt.NewClientOptions().
		AddBroker(fmt.Sprintf("tls://%s:%s", os.Getenv("MQTT_HOST"), os.Getenv("MQTT_PORT"))).
		SetClientID(os.Getenv("MQTT_CLIENT_ID")).
		SetUsername(os.Getenv("MQTT_USER")).
		SetPassword(os.Getenv("MQTT_PASS")).
		SetConnectionLostHandler(func(client mqtt.Client, err error) {
			log.Warn().Err(err).Msg("Connection lost to broker")
		}).
		SetOnConnectHandler(func(client mqtt.Client) {
			log.Debug().Msg("Connected to broker")
		}).
		SetBinaryWill(
			os.Getenv("SERVER_WILL_TOPIC"), []byte("{\"message\":\"server disconnected\"}"), 2, true,
		)

	return mqtt.NewClient(opts)
}

func Subscribe(wg *sync.WaitGroup, client mqtt.Client, topic string, qos byte, callback func(client mqtt.Client, resp mqtt.Message)) {
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

func Publish(wg *sync.WaitGroup, client mqtt.Client, topic string, payload string) {
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
