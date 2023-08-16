package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var debug *bool

//////// Borrowed from https://github.com/YuukanOO/rtv

// TVInfo represents a remote TV.
type TVInfo struct {
	//Model string
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
	return controller.handle.Close()
}

//////// End borrow

type SamsungRemoteMQTTBridge struct {
	MQTTClient  mqtt.Client
	Controller  *SamsungController
	NetworkInfo *NetworkInfo
	TVInfo      *TVInfo
}

func NewSamsungRemoteMQTTBridge(tvIPAddress *string /* tvModel *string, */, mqttBroker string) *SamsungRemoteMQTTBridge {

	networkInfo, err := getNetworkInformations()
	if err != nil {
		panic(err)
	}

	tv := &TVInfo{
		//Model: *tvModel,
		IP: net.ParseIP(*tvIPAddress),
	}

	controller := NewSamsungController()
	err = controller.Connect(networkInfo, tv)
	if err != nil {
		panic(err)
	}
	defer controller.Close()

	// err = controller.SendKey(nInfo, tv, os.Args[3])
	// if err != nil {
	// 	panic(err)
	// }

	opts := mqtt.NewClientOptions().AddBroker(mqttBroker)
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	} else if *debug {
		fmt.Printf("Connected to MQTT broker: %s\n", mqttBroker)
	}

	bridge := &SamsungRemoteMQTTBridge{
		MQTTClient:  client,
		Controller:  controller,
		NetworkInfo: networkInfo,
		TVInfo:      tv,
	}

	funcs := map[string]func(client mqtt.Client, message mqtt.Message){
		"samsungremote/key/send": bridge.onKeySend,
	}
	for key, function := range funcs {
		token := client.Subscribe(key, 0, function)
		token.Wait()
	}
	time.Sleep(2 * time.Second)
	bridge.initialize(true)
	return bridge
}

func (bridge *SamsungRemoteMQTTBridge) initialize(askPower bool) {
}

func (bridge *SamsungRemoteMQTTBridge) onKeySend(client mqtt.Client, message mqtt.Message) {
	p := string(message.Payload())
	bridge.PublishMQTT("samsungremote/key/send", "", false)
	bridge.Controller.SendKey(bridge.NetworkInfo, bridge.TVInfo, p)
}

func (bridge *SamsungRemoteMQTTBridge) PublishMQTT(topic string, message string, retained bool) {
	token := bridge.MQTTClient.Publish(topic, 0, retained, message)
	token.Wait()
}

func (bridge *SamsungRemoteMQTTBridge) SerialLoop() {
	// buf := make([]byte, 128)
	// for {
	// 	n, err := bridge.SerialPort.Read(buf)
	// 	if err != nil {
	// 		log.Fatal(err)
	// 	}
	// 	bridge.ProcessRotelData(string(buf[:n]))

	// 	jsonState, err := json.Marshal(bridge.State)
	// 	if err != nil {
	// 		continue
	// 	}
	// 	bridge.PublishMQTT("rotel/state", string(jsonState), true)
	// }
}

func printHelp() {
	fmt.Println("Usage: rotel-mqtt [OPTIONS]")
	fmt.Println("Options:")
	flag.PrintDefaults()
}

func main() {
	tvIPAddress := flag.String("ip", "", "TV IP address")
	//tvModel := flag.String("model", "", "TV model")
	mqttBroker := flag.String("broker", "tcp://localhost:1883", "MQTT broker URL")
	help := flag.Bool("help", false, "Print help")
	debug = flag.Bool("debug", false, "Debug logging")
	flag.Parse()

	if *help {
		printHelp()
		os.Exit(0)
	}

	//bridge := NewSamsungRemoteMQTTBridge(tvIPAddress, tvModel, *mqttBroker)
	bridge := NewSamsungRemoteMQTTBridge(tvIPAddress, *mqttBroker)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	fmt.Printf("Started\n")
	go bridge.SerialLoop()
	<-c
	fmt.Printf("Shut down\n")
	os.Exit(0)
}
