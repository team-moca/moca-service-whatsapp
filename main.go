package main

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rhymen/go-whatsapp"

	qrcodeTerminal "github.com/Baozisoftware/qrcode-terminal-go"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

var client mqtt.Client

type Chat struct {
	ChatId       string   `json:"chat_id"`
	Name         string   `json:"name"`
	ChatType     string   `json:"chat_type"`    // ChatType.single or ChatType.group
	Participants []string `json:"participants"` // IDs of participants
}

type Contact struct {
	ContactId string `json:"contact_id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
}

var _storage = make(map[int]Session)

func Get(sessionId int) (Session, error) {

	s, found := _storage[sessionId]
	if !found {
		fmt.Printf("Session not in cache. Try to load it from disk now...\n")

		s, err := NewSession(sessionId, whatsapp.Session{})

		if err != nil {
			fmt.Println(err)
		}

		path := ".sessions/" + strconv.Itoa(sessionId) + ".gob"

		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Session does not yet exist

			_storage[sessionId] = s
			return s, nil
		}

		file, err := os.Open(path)
		if err != nil {
			return s, err
		}
		defer file.Close()

		wa := whatsapp.Session{}

		decoder := gob.NewDecoder(file)
		if err = decoder.Decode(&wa); err != nil {
			return s, err
		}

		s.WhatsApp = wa

		_storage[sessionId] = s

		return s, nil
	}

	return s, nil
}

func Save(s Session) error {
	_storage[s.SessionId] = s

	file, err := os.Create(".sessions/" + strconv.Itoa(s.SessionId) + ".gob")
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := gob.NewEncoder(file)
	if err = encoder.Encode(s.WhatsApp); err != nil {
		return err
	}

	return nil
}

type Session struct {
	SessionId int
	WhatsApp  whatsapp.Session
	Conn      *whatsapp.Conn
}

func NewSession(sessionId int, ws whatsapp.Session) (Session, error) {

	conn, err := whatsapp.NewConnWithOptions(&whatsapp.Options{
		ShortClientName: "MOCA",
		LongClientName:  "MOCA Service for WhatsApp",
		ClientVersion:   "0.1",
		Timeout:         5 * time.Second,
	})

	if err != nil {
		return Session{}, fmt.Errorf("Error creating session: %v", err)
	}

	conn.AddHandler(&Handler{ws, conn, sessionId, uint64(time.Now().Unix())})

	fmt.Printf("Starting session for #%d\n", sessionId)

	return Session{sessionId, ws, conn}, nil
}

func waLogin(sess Session) error {

	if reflect.ValueOf(sess.WhatsApp).IsZero() {

		fmt.Println("No previous session found. Logging in via QR code...")

		qr := make(chan string)

		go func() {
			terminal := qrcodeTerminal.New2(qrcodeTerminal.ConsoleColors.BrightBlack, qrcodeTerminal.ConsoleColors.BrightWhite, qrcodeTerminal.QRCodeRecoveryLevels.Low)
			terminal.Get(<-qr).Print()
		}()

		was, err := sess.Conn.Login(qr)
		if err != nil {
			return fmt.Errorf("error during login: %v", err)
		}

		sess.WhatsApp = was

		err = Save(sess)
		if err != nil {
			return fmt.Errorf("error during login: %v", err)
		}

		fmt.Println("Login seemed to have been successful.")

	} else {

		_, err := sess.Conn.RestoreWithSession(sess.WhatsApp)
		if err != nil {
			fmt.Println("Already logged in.")
		}

		// TODO Add message listeners here
		fmt.Println("Session restoration seemed to have been successful.")

	}

	return nil
}

func getPhoneFromWid(wid string) string {
	return "+" + strings.Split(wid, "@")[0]
}

func Configure(flowId int, rawMessage []byte) ([]byte, error) {
	fmt.Printf("Configuring #%d with data: %s", flowId, rawMessage)
	fmt.Println()

	sess, err := Get(flowId)

	if err != nil {
		return nil, err
	}

	err = waLogin(sess)

	if err != nil {
		return nil, fmt.Errorf("error while configuring this service: %v", err)
	}

	// get self contact
	phone := getPhoneFromWid(sess.Conn.Info.Wid)

	msg := "{\"step\": \"finished\", \"data\": {\"contact\": {\"contact_id\": \"" + sess.Conn.Info.Wid + "\", \"name\": \"" + sess.Conn.Info.Pushname + "\", \"phone\": \"" + phone + "\"}}}"

	return []byte(msg), nil
}

type Handler struct {
	waSession whatsapp.Session
	conn      *whatsapp.Conn
	sessionId int
	startTime uint64
}

var f mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {
	fmt.Printf("TOPIC: %s\n", msg.Topic())
	fmt.Printf("MSG: %s\n", msg.Payload())
}

func (Handler) HandleError(err error) {
	fmt.Fprintf(os.Stderr, "error caught in handler: %v\n", err)
}

// HandleTextMessage receives whatsapp text messages and checks if the message was send by the current
// user, if it does not contain the keyword '@echo' or if it is from before the program start and then returns.
// Otherwise the message is echoed back to the original author.
// func (h Handler) HandleTextMessage(message whatsapp.TextMessage) {

// 	fmt.Printf("[WA-%v TM] [%v]: %v from %v\n", h.sessionId, message.Info.Id, message.Text, message.Info.RemoteJid)
// }

// HandleContactList is a list of chats and groups the user can write to.
func (h Handler) HandleContactList(contacts []whatsapp.Contact) {
	response := make([]Contact, 0)
	for _, contact := range contacts {

		fmt.Printf("[WA-%v CoL] [%v]: %v / %v\n", h.sessionId, contact.Jid, contact.Name, contact.Short)

		if strings.HasSuffix(contact.Jid, "@c.us") || strings.HasSuffix(contact.Jid, "@s.whatsapp.net") {
			// Chat is a 1:1 chat

			response = append(response, Contact{
				contact.Jid,
				contact.Name,
				getPhoneFromWid(contact.Jid),
			})
		} else if strings.HasSuffix(contact.Jid, "@g.us") {
			// Chat is a group chat
		} else if strings.HasSuffix(contact.Jid, "@broadcast") {
			// Chat is a broadcast
		} else {
			fmt.Printf("[WA-%v CoL] [%v]: Error: Unknown contact type.\n", h.sessionId, contact.Jid)
		}
	}
	b, err := json.Marshal(response)

	if err != nil {
		fmt.Printf("[WA-%v CoL]: Error: %v\n", h.sessionId, err)
	} else {
		client.Publish("moca/via/whatsapp/"+strconv.Itoa(h.sessionId)+"/contacts", 2, false, b)
	}

}

// HandleChatList is a list of chats that the user has written with.
func (h Handler) HandleChatList(chats []whatsapp.Chat) {
	response := make([]Chat, 0)
	for _, chat := range chats {

		fmt.Printf("[WA-%v ChL] [%v]: %v\n", h.sessionId, chat.Jid, chat.Name)

		if strings.HasSuffix(chat.Jid, "@c.us") || strings.HasSuffix(chat.Jid, "@s.whatsapp.net") {
			// Chat is a 1:1 chat

			response = append(response, Chat{
				chat.Jid,
				chat.Name,
				"ChatType.single",
				[]string{h.conn.Info.Wid, chat.Jid},
			})
		} else if strings.HasSuffix(chat.Jid, "@g.us") {
			// Chat is a group chat
		} else if strings.HasSuffix(chat.Jid, "@broadcast") {
			// Chat is a broadcast
		} else {
			fmt.Printf("[WA-%v ChL] [%v]: Error: Unknown chat type.\n", h.sessionId, chat.Jid)
		}
	}
	b, err := json.Marshal(response)

	if err != nil {
		fmt.Printf("[WA-%v ChL]: Error: %v\n", h.sessionId, err)
	} else {
		client.Publish("moca/via/whatsapp/"+strconv.Itoa(h.sessionId)+"/chats", 2, false, b)
	}
}

func (h Handler) HandleNewContact(contact whatsapp.Contact) {

	fmt.Printf("[WA-%v NC] [%v]: %v (%v)\n", h.sessionId, contact.Jid, contact.Name, contact.Short)
}

// func (Handler) HandleJsonMessage(message string) {
// 	fmt.Println("JSON: %w", message)
// }

func cleanup() {
	fmt.Println("Cleanup now.")
	client.Disconnect(250)
}

// handleConfigureService handles mqtt messages for the topic `whatsapp/{connector_id}/{uuid}/configure`.
// It will publish a response `whatsapp/{connector_id}/{uuid}/configure/response` which contains a `step` id and the required data described in `schema` as a JSON schema.
func handleConfigureService(client mqtt.Client, msg mqtt.Message) {
	fmt.Printf("handleConfigureService      ")
	fmt.Printf("[%s]  ", msg.Topic())
	fmt.Printf("%s\n", msg.Payload())
	fmt.Println()

	parts := strings.Split(msg.Topic(), "/")

	connector_id, err := strconv.Atoi(parts[1])

	if err != nil {
		fmt.Println("connector_id must be an int.")
	}

	response, err := Configure(connector_id, msg.Payload())

	if err != nil {
		fmt.Println("Cannot send response: " + err.Error())
		return
	}

	client.Publish(msg.Topic()+"/response", 2, false, response)
}

func handleGetContact(client mqtt.Client, msg mqtt.Message) {
	fmt.Printf("handleGetContact      ")
	fmt.Printf("[%s]  ", msg.Topic())
	fmt.Printf("%s\n", msg.Payload())
	fmt.Println()

	parts := strings.Split(msg.Topic(), "/")

	contact_id := parts[4]
	phone := getPhoneFromWid(contact_id)

	b, err := json.Marshal(Contact{
		contact_id,
		"",
		phone,
	})

	if err != nil {
		fmt.Println("Cannot send response: " + err.Error())
		return
	}

	client.Publish(msg.Topic()+"/response", 2, false, b)
}

func main() {
	//mqtt.DEBUG = log.New(os.Stdout, "", 0)
	mqtt.ERROR = log.New(os.Stdout, "", 0)
	opts := mqtt.NewClientOptions().AddBroker("tcp://localhost:1883").SetClientID("WA-" + uuid.New().String())
	opts.SetKeepAlive(2 * time.Second)
	opts.SetDefaultPublishHandler(f)
	opts.SetPingTimeout(1 * time.Second)
	opts.SetCleanSession(true) // DEBUG only!

	client = mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	if token := client.Subscribe("whatsapp/+/+/configure", 0, handleConfigureService); token.Wait() && token.Error() != nil {
		fmt.Println(token.Error())
		os.Exit(1)
	}

	if token := client.Subscribe("whatsapp/+/+/get_contact/+", 0, handleGetContact); token.Wait() && token.Error() != nil {
		fmt.Println(token.Error())
		os.Exit(1)
	}

	death := make(chan os.Signal)
	signal.Notify(death, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-death
		cleanup()
		os.Exit(1)
	}()

	for {
		time.Sleep(10 * time.Second) // or runtime.Gosched() or similar per @misterbee
	}
}
