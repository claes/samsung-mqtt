package lib

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// TVInfo represents a remote TV.
type TVInfo struct {
	IP net.IP
}

// NetworkInfo represents a device on the network.
type NetworkInfo struct {
	IP  net.IP
	MAC string
}

// Controller is the base interface implemented by vendor specific TVs.
type Controller interface {
	Connect(emitter *NetworkInfo, receiver *TVInfo) error
	SendKey(emitter *NetworkInfo, receiver *TVInfo, key string) error
	Close() error
}

func getNetworkInformations() (*NetworkInfo, error) {
	interfaces, err := net.Interfaces()

	if err != nil {
		return nil, err
	}

	for _, v := range interfaces {
		if v.Flags&net.FlagLoopback == 0 && !strings.Contains(v.Name, "vir") {
			addresses, err := v.Addrs()

			if err != nil {
				return nil, err
			}
			for _, i := range addresses {

				var ip net.IP
				switch v := i.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}

				return &NetworkInfo{
					IP:  ip,
					MAC: v.HardwareAddr.String(),
				}, nil
			}
		}
	}
	return nil, nil
}

// SamsungController represents a controller for samsung smart tvs.
type SamsungController struct {
	appString  string
	remoteName string
	handle     *net.TCPConn
}

// NewSamsungController instantiates a new controller for samsung smart TVs.
func NewSamsungController() *SamsungController {
	return &SamsungController{
		appString:  "iphone..iapp.samsung",
		remoteName: "MQTT Bridge",
	}
}

// Connect initialize the connection.
func (controller *SamsungController) Connect(emitter *NetworkInfo, receiver *TVInfo) error {
	conn, err := net.DialTCP("tcp", &net.TCPAddr{
		IP: emitter.IP,
	}, &net.TCPAddr{
		IP:   receiver.IP,
		Port: 55000,
	})

	if err != nil {
		return err
	}

	controller.handle = conn

	encoding := base64.StdEncoding

	encodedIP := encoding.EncodeToString([]byte(emitter.IP.String()))
	encodedMAC := encoding.EncodeToString([]byte(emitter.MAC))
	encodedRemoteName := encoding.EncodeToString([]byte(controller.remoteName))

	msgPart1 := fmt.Sprintf("%c%c%c%c%s%c%c%s%c%c%s", 0x64, 0x00, len(encodedIP), 0x00, encodedIP, len(encodedMAC), 0x00, encodedMAC, len(encodedRemoteName), 0x00, encodedRemoteName)
	part1 := fmt.Sprintf("%c%c%c%s%c%c%s", 0x00, len(controller.appString), 0x00, controller.appString, len(msgPart1), 0x00, msgPart1)

	_, err = controller.handle.Write([]byte(part1))

	if err != nil {
		return err
	}

	msgPart2 := fmt.Sprintf("%c%c", 0xc8, 0x00)
	part2 := fmt.Sprintf("%c%c%c%s%c%c%s", 0x00, len(controller.appString), 0x00, controller.appString, len(msgPart2), 0x00, msgPart2)

	_, err = controller.handle.Write([]byte(part2))

	return err
}

// SendKey sends a key to the TV.
func (controller *SamsungController) SendKey(emitter *NetworkInfo, receiver *TVInfo, key string) error {
	encoding := base64.StdEncoding
	encodedKey := encoding.EncodeToString([]byte(key))

	msgPart3 := fmt.Sprintf("%c%c%c%c%c%s", 0x00, 0x00, 0x00, len(encodedKey), 0x00, encodedKey)
	part3 := fmt.Sprintf("%c%c%c%s%c%c%s", 0x00, len(controller.appString), 0x00, controller.appString, len(msgPart3), 0x00, msgPart3)

	_, err := controller.handle.Write([]byte(part3))

	return err
}

// Close the connection.
func (controller *SamsungController) Close() error {
	slog.Info("Closing controller")
	return controller.handle.Close()
}

type SamsungRemoteMQTTBridge struct {
	MQTTClient  mqtt.Client
	TopicPrefix string
	Controller  *SamsungController
	NetworkInfo *NetworkInfo
	TVInfo      *TVInfo
}

func NewSamsungRemoteMQTTBridge(tvIPAddress *string, mqttClient mqtt.Client, topicPrefix string) *SamsungRemoteMQTTBridge {

	networkInfo, err := getNetworkInformations()
	if err != nil {
		panic(err)
	}

	tv := &TVInfo{
		IP: net.ParseIP(*tvIPAddress),
	}

	controller := NewSamsungController()
	err = controller.Connect(networkInfo, tv)
	if err != nil {
		slog.Error("Could not connect to Samsung TV (it may be off)", "ip", *tvIPAddress, "error", err)
		reconnectSamsungTV = true
	}
	slog.Debug("Connected to Samsung TV", "ip", *tvIPAddress)

	bridge := &SamsungRemoteMQTTBridge{
		MQTTClient:  mqttClient,
		TopicPrefix: topicPrefix,
		Controller:  controller,
		NetworkInfo: networkInfo,
		TVInfo:      tv,
	}

	funcs := map[string]func(client mqtt.Client, message mqtt.Message){
		"samsungremote/key/send":          bridge.onKeySend,
		"samsungremote/key/reconnectsend": bridge.onKeyReconnectSend,
	}
	for key, function := range funcs {
		token := mqttClient.Subscribe(topicPrefix+"/"+key, 0, function)
		token.Wait()
	}
	time.Sleep(2 * time.Second)
	return bridge
}

func CreateMQTTClient(mqttBroker string) mqtt.Client {
	slog.Info("Creating MQTT client", "broker", mqttBroker)
	opts := mqtt.NewClientOptions().AddBroker(mqttBroker)
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		slog.Error("Could not connect to broker", "mqttBroker", mqttBroker, "error", token.Error())
		panic(token.Error())
	}
	slog.Info("Connected to MQTT broker", "mqttBroker", mqttBroker)
	return client
}

var reconnectSamsungTV = false

func (bridge *SamsungRemoteMQTTBridge) reconnectIfNeeded() {
	if reconnectSamsungTV {
		err := bridge.Controller.Connect(bridge.NetworkInfo, bridge.TVInfo)
		if err != nil {
			slog.Debug("Could not reconnect", "error", err)
		} else {
			slog.Debug("Reconnection successful")
			reconnectSamsungTV = false
		}
	}
}

var sendMutex sync.Mutex

func (bridge *SamsungRemoteMQTTBridge) onKeySend(client mqtt.Client, message mqtt.Message) {
	sendMutex.Lock()
	defer sendMutex.Unlock()

	command := string(message.Payload())
	if command != "" {
		bridge.PublishMQTT("samsungremote/key/send", "", false)
		slog.Debug("Sending key", "key", command)

		err := bridge.Controller.SendKey(bridge.NetworkInfo, bridge.TVInfo, command)
		if err != nil {
			slog.Debug("Sending key, attempt reconnect", "key", command)
			reconnectSamsungTV = true
		}
	}
}

func (bridge *SamsungRemoteMQTTBridge) onKeyReconnectSend(client mqtt.Client, message mqtt.Message) {
	sendMutex.Lock()
	defer sendMutex.Unlock()

	command := string(message.Payload())
	if command != "" {
		bridge.PublishMQTT("samsungremote/key/reconnectsend", "", false)
		slog.Debug("Sending key", "key", command)

		reconnectSamsungTV = true
		bridge.reconnectIfNeeded()
		bridge.Controller.SendKey(bridge.NetworkInfo, bridge.TVInfo, command)
	}
}

func (bridge *SamsungRemoteMQTTBridge) PublishMQTT(subtopic string, message string, retained bool) {
	token := bridge.MQTTClient.Publish(bridge.TopicPrefix+"/"+subtopic, 0, retained, message)
	token.Wait()
}

func (bridge *SamsungRemoteMQTTBridge) MainLoop() {
	go func() {
		for {
			time.Sleep(8 * time.Second)
			bridge.reconnectIfNeeded()
		}
	}()
}
