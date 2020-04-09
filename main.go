package main

import (
	"context"
	"github.com/rsocket/rsocket-go"
	"github.com/rsocket/rsocket-go/payload"
	"github.com/rsocket/rsocket-go/rx"
	"github.com/rsocket/rsocket-go/rx/flux"
	"github.com/rsocket/rsocket-go/rx/mono"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// tworze liste hostow do polaczenia
var exampleChat = []string{
	"tcp://10.5.0.3:7878",
	"tcp://10.5.0.4:7878",
}

type Chat struct {
	ID             string
	clientsIPsLIST []string
	messages       chan payload.Payload
	listiner       interface{}
	f              flux.Flux
}

func NewChat(ID string, clientsIPsLIST []string) *Chat {
	return &Chat{ID: ID, clientsIPsLIST: clientsIPsLIST, messages: make(chan payload.Payload)}
}

type User struct {
	userIP          string
	clientsIPs      map[string]bool           // clientIP : status
	clientsSockets  map[rsocket.Client]string // socket : clientIP
	chatList        map[string]*Chat // chatID, *Chat
	sendMessageList map[string]chan payload.Payload // payload and target chat format: map[clientIP] payload(message, chatID)
}

func NewUser() *User {
	userAddr := "tcp://127.0.0.2:7878"

	if value, ok := os.LookupEnv("USER_ADDR"); ok {
		userAddr = value
		log.Println("my address: " + userAddr)
	}

	_clientsIPs     := make(map[string]bool)
	_clientsSockets := make(map[rsocket.Client]string)
	_chatList       := make(map[string]*Chat)
	_sendMessageList:= make(map[string]chan payload.Payload)

	return &User{
		userIP:          userAddr,
		clientsIPs:      _clientsIPs,
		clientsSockets:  _clientsSockets,
		chatList:        _chatList,
		sendMessageList: _sendMessageList,
	}
}

func (u *User) sendMessage(chatID string, msg payload.Payload) {
	log.Println("sendMessage: ", msg.DataUTF8())

	chatHandle := u.chatList[chatID]

	userList := chatHandle.clientsIPsLIST

	// TODO how to handle sending to oneself? loopback?
	for _, it := range userList {
		log.Println("sendMessage: begin loop ", it)
		if strings.EqualFold(u.userIP, it) {
			log.Println("sendMessage: send to ", chatHandle.messages)
			chatHandle.messages <- msg
		} else {
			log.Println("sendMessage: send to ", u.sendMessageList[it], " to ", it)
			u.sendMessageList[it] <- msg
		}
	}
}

func (u *User) createChat(initList []string) {

	chatIDstr := "123"

	// init new chat with complete users list
	// add userIP ex"tcp://10.5.0.2:7878" to that list
	tmpChat := NewChat(chatIDstr, append(initList, u.userIP))

	go tmpChat.messagePrinter()

	// TODO TMP IMPLEMENTATION WARNING
	// not working if already connected to this user
	// get all users IP I want to connect
	for _, cli := range initList {
		u.clientsIPs[cli] = false
	}

	u.chatList[chatIDstr] = tmpChat

}

func (u *User) connectionsHandler() {
	// create for each goroutine that handles channel
	// create new chan and give it
	for {
		for userAddr := range u.clientsIPs {
			// if not connected
			if !u.clientsIPs[userAddr] {
				// find if chan for that client exists
				// TODO can be written better
				if u.sendMessageList[userAddr] == nil {
					log.Println("connectionsHandler: chan non existing - creating ", userAddr)
					ch := make(chan payload.Payload)
					u.sendMessageList[userAddr] = ch
				}
				go u.connectToClient(u.sendMessageList[userAddr], userAddr)
				u.clientsIPs[userAddr] = true
			}
		}
		// sleep maybe
	}
	// close(chan)
}

// Possible type problem: struct vs payload
func (u *User) connectToClient(ch chan payload.Payload, addr string) {
	// goroutine for connecting to clients
	// handle channels

	// in advanced scenario ask host for chat clients ips

	// create tmp flux
	// TODO problem: who is the target
	f := flux.Create(func(ctx context.Context, s flux.Sink) {
		log.Println("STARTED sending new message")
		for mess := range ch {
			log.Println("SENDING new message")
			s.Next(mess)
		}
		s.Complete()
	})

	// new client
	// TODO change literals to constants
	cli, err := rsocket.
		Connect().
		SetupPayload(payload.NewString(u.userIP,"1234")).
		Resume().
		Fragment(1024).
		Transport(addr).
		Start(context.Background())
	if err != nil {
		panic(err)
	}

	defer cli.Close()

	log.Println("REQUESTING CHANNEL WITH ", addr)

	// possible error
	_, err = cli.RequestChannel(f).
		DoOnNext(func(elem payload.Payload) {
			log.Println("GOT new message")
			tmpChatID, _ := elem.MetadataUTF8()
			u.chatList[tmpChatID].messages <- elem
		}).
		BlockLast(context.Background())
}

func (u *User) eventListener() {
	// await for new connections
	err := rsocket.Receive().
		Resume().
		Fragment(1024).
		Acceptor(func(setup payload.SetupPayload, sendingSocket rsocket.CloseableRSocket) (rsocket.RSocket, error) {
			log.Println("GOT REQUEST ", setup.DataUTF8())
			sendingSocket.OnClose(func(err error) {
				log.Println("***** socket disconnected *****", err, " ", setup.DataUTF8())
			})
			return u.responder(setup), nil
		}).
		Transport(u.userIP).
		Serve(context.Background())
	panic(err)

	// on that event update clientsIPs etc
}

func (u *User) responder(setup payload.SetupPayload) rsocket.RSocket {
	// custom responder
	return rsocket.NewAbstractSocket(
		rsocket.MetadataPush(func(item payload.Payload) {
			log.Println("GOT METADATA_PUSH:", item)
		}),
		rsocket.FireAndForget(func(elem payload.Payload) {
			log.Println("GOT FNF:", elem)
		}),
		rsocket.RequestResponse(func(pl payload.Payload) mono.Mono {
			if meta, _ := pl.MetadataUTF8(); strings.EqualFold(meta, "REJECT_ME") {
				return nil
			}

			return mono.Just(pl)
		}),
		rsocket.RequestStream(func(pl payload.Payload) flux.Flux {
			s := pl.DataUTF8()
			m, _ := pl.MetadataUTF8()
			log.Println("data:", s, "metadata:", m)

			// handle getHosts request
			if dat, _ := pl.MetadataUTF8(); strings.EqualFold(dat, "CHAT_PARTICIPANTS_REQ") { // [chatID, REQ type]
				return flux.Create(func(ctx context.Context, emitter flux.Sink) {
					for _, ip := range u.chatList[pl.DataUTF8()].clientsIPsLIST {
						emitter.Next(payload.NewString(ip, "CHAT_PARTICIPANTS_RESP"))
					}
					emitter.Complete()
				})
			}

			return flux.Create(func(ctx context.Context, emitter flux.Sink) { emitter.Next(payload.NewString("EMPTY", "EMPTY")) })
		}),
		// TODO
		rsocket.RequestChannel(func(inputs rx.Publisher) flux.Flux {
			// control connected hosts:
			// get connecting hostIP and update user array

			// format: setup[clientIP]

			u.clientsIPs[setup.DataUTF8()] = true

			// create new chat
			u.createChat([]string{})

			inputs.(flux.Flux).DoFinally(func(s rx.SignalType) {
				log.Printf("signal type: %v", s)
				//close(receives)
			}).Subscribe(context.Background(), rx.OnNext(func(input payload.Payload) {
				log.Println("GOT MESSAGE: ", input.DataUTF8())
				tmpChatID, _ := input.MetadataUTF8()
				log.Println("responder: is channel nil?: ", u.chatList[tmpChatID].messages == nil)
				u.chatList[tmpChatID].messages <- input
			}))

			return flux.Create(func(ctx context.Context, s flux.Sink) {
				for mess := range u.sendMessageList[setup.DataUTF8()] {
					s.Next(mess)
				}
				s.Complete()
			})
		}),
	)
}

func (u *User) writeDemoMessages(chatID string) {
	go func() {
		log.Println("writeDemoMessages: just started")
		for i := 0; i < 100; i++ {
			//log.Println("writeDemoMessages: Sent message" + strconv.Itoa(i))
			time.Sleep(time.Second * 2)
			u.sendMessage(chatID, payload.NewString("message " +  strconv.Itoa(i), chatID))
		}
	}()
}

func (c *Chat) messagePrinter() {
	for {
		if c.messages != nil {
			log.Println("messagePrinter: ", <-c.messages)
		}
	}
}

func main() {
	value, _ := os.LookupEnv("MACHINE_NUM")

	user := NewUser()
	// nowy czat
	go user.eventListener()
	go user.connectionsHandler()

	log.Println(value + ": started new chat")
	log.Println(value + ": started chatListiner")

	// lacze sie z ludzmi
	if val, _ := os.LookupEnv("MACHINE_NUM"); val == "1" {
		time.Sleep(time.Second * 3)
		log.Println(value + ": started createChat")
		user.createChat(exampleChat) // chatID = 123
		time.Sleep(time.Second * 3)
		log.Println(value + ": started sending messages")
		user.writeDemoMessages("123")
	}

	if value, _ := os.LookupEnv("MACHINE_NUM"); value == "2" {
		//time.Sleep(time.Second * 10)

	}

	log.Println(value + ": listening for messages: ")

	//for mess := range chat.messages {
	//	log.Println("Got message" + mess.DataUTF8())
	//}



	time.Sleep(time.Minute * 2)
}
