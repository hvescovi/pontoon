package pontoon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type HTTPTransport struct {
	Address  string
	node     *Node
	listener net.Listener
}

var leaderIP string

const TIMEOUT = time.Duration(time.Second)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func (t *HTTPTransport) Serve(node *Node) error {
	t.node = node

	httpListener, err := net.Listen("tcp", t.Address)
	if err != nil {
		log.Fatalf("FATAL: listen (%s) failed - %s", t.Address, err)
	}
	t.listener = httpListener

	go func() {
		log.Printf("[%s] starting HTTP server", t.node.ID)
		server := &http.Server{
			Handler: t,
		}
		err := server.Serve(httpListener)
		// theres no direct way to detect this error because it is not exposed
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("ERROR: http.Serve() - %s", err)
		}
		close(t.node.exitChan)
		log.Printf("[%s] exiting Serve()", t.node.ID)
	}()

	return nil
}

func (t *HTTPTransport) Close() error {
	return t.listener.Close()
}

func (t *HTTPTransport) String() string {
	return t.listener.Addr().String()
}

func (t *HTTPTransport) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/ping":
		t.pingHandler(w, req)

	case "/request_vote":
		t.requestVoteHandler(w, req)

	case "/append_entries":
		t.appendEntriesHandler(w, req)

	case "/command":
		t.commandHandler(w, req)

	case "/print":
		apiResponse(w, 299, t.node.Log.PrintAll())

	case "/cluster":
		var str string
		for _, peer := range t.node.Cluster {
			str += peer.ID + " "
		}
		apiResponse(w, 299, str)

	case "/node":
		apiResponse(w, 299, strconv.Itoa(t.node.State))

	case "/leader":
		apiResponse(w, 299, findLeaderIP(t.node))

	case "/ip":
		apiResponse(w, 299, t.Address)

	case "/hash":
		hash := sha256.Sum256([]byte(t.node.Log.PrintAll()))
		apiResponse(w, 299, hex.EncodeToString(hash[:]))

	case "/len":
		apiResponse(w, 299, t.node.Log.Length())

	case "/die":
		apiResponse(w, 299, "Exiting")
		go func() {
			time.Sleep(time.Second)
			os.Exit(1)
		}()

	case "/killleader":
		strcommand := findLeaderIP(t.node) + "/die"

		fmt.Fprintln(w, strcommand)

		http.Get(strcommand)

		apiResponse(w, 299, "Killleader sent")

	case "/dieifnotleader":
		if t.node.State == Leader {
			apiResponse(w, 299, "I am the leader!")
		} else {
			apiResponse(w, 299, "Exiting")

			go func() {
				time.Sleep(time.Second)
				os.Exit(1)
			}()
		}

	case "/dieifleader":
		if t.node.State != Leader {
			apiResponse(w, 299, "I am not the leader!")
		} else {
			apiResponse(w, 299, "Exiting")

			go func() {
				time.Sleep(time.Second)
				os.Exit(1)
			}()
		}

	default:
		command := strings.Split(req.URL.Path[1:], "/")
		if command[0] == "request" {
			handleRequest(command[1:], t.node, w)
		} else {
			apiResponse(w, 403, "Erro no comando")
		}
	}
}

func handleRequest(args []string, node *Node, w http.ResponseWriter) {
	if node.State == Leader {
		respChan := make(chan CommandResponse, 1)

		var id int64
		var body []byte

		if len(args) < 2 {
			id = rand.Int63()
			body = randomByteString(PAYLOAD_SIZE)
		} else {
			id32, _ := strconv.Atoi(args[1])

			id = int64(id32)

			body = []byte(strings.Join(args[2:], "/"))
		}

		cr := CommandRequest{
			ID:           id,
			Name:         "SUP",
			Body:         body,
			ResponseChan: respChan,
		}

		node.Command(cr)

		if (<-respChan).Success {
			apiResponse(w, 200, "Sucesso!")
		} else {
			apiResponse(w, 500, "Erro no request")
		}

	} else {
		statusCode := requestToLeader("request"+strings.Join(args, "/"), node, w)

		apiResponse(w, statusCode, "Sucesso!")
	}
}

func requestToLeader(command string, node *Node, w http.ResponseWriter) int {
	client := http.Client{
		Timeout: TIMEOUT,
	}

	leaderIP = findLeaderIP(node)

	resp, err := client.Get("http://" + leaderIP + "/" + command)

	if err != nil || resp.StatusCode != 200 {

		fmt.Fprintf(w, "%v\n%v\n%v\n\n", err, resp, leaderIP+"/"+command)

		return requestToLeader(command, node, w)
	}

	resp.Body.Close()

	return resp.StatusCode
}

func apiResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	response, err := json.Marshal(data)
	if err != nil {
		response = []byte("INTERNAL_ERROR")
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(response)))
	w.WriteHeader(statusCode)
	w.Write(response)
}

func (t *HTTPTransport) pingHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Length", "2")
	io.WriteString(w, "OK")
}

func (t *HTTPTransport) requestVoteHandler(w http.ResponseWriter, req *http.Request) {
	var vr VoteRequest

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		apiResponse(w, 500, nil)
		return
	}

	err = json.Unmarshal(data, &vr)
	if err != nil {
		apiResponse(w, 500, nil)
		return
	}

	resp, err := t.node.RequestVote(vr)
	if err != nil {
		apiResponse(w, 500, nil)
		return
	}

	apiResponse(w, 200, resp)
}

func (t *HTTPTransport) appendEntriesHandler(w http.ResponseWriter, req *http.Request) {
	var er EntryRequest

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		apiResponse(w, 500, nil)
		return
	}

	err = json.Unmarshal(data, &er)
	if err != nil {
		apiResponse(w, 500, nil)
		return
	}

	resp, err := t.node.AppendEntries(er)
	if err != nil {
		apiResponse(w, 500, nil)
	}

	apiResponse(w, 200, resp)
}

// TODO: split this out into peer transport and client transport
// move into client transport
func (t *HTTPTransport) commandHandler(w http.ResponseWriter, req *http.Request) {
	var cr CommandRequest

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		apiResponse(w, 500, "Sua requisicao esta vazia")
		return
	}

	err = json.Unmarshal(data, &cr)
	if err != nil {
		apiResponse(w, 500, "Unmarshall error")
		return
	}

	cr.ResponseChan = make(chan CommandResponse, 1)
	t.node.Command(cr)
	resp := <-cr.ResponseChan
	if !resp.Success {
		apiResponse(w, 500, "Comando nao foi bem sucedido")
	}

	apiResponse(w, 200, resp)
}

func (t *HTTPTransport) RequestVoteRPC(address string, voteRequest VoteRequest) (VoteResponse, error) {
	endpoint := fmt.Sprintf("http://%s/request_vote", address)
	log.Printf("[%s] RequestVoteRPC %+v to %s", t.node.ID, voteRequest, endpoint)
	data, err := apiRequest("POST", endpoint, voteRequest, 100*time.Millisecond)
	if err != nil {
		return VoteResponse{}, err
	}
	term, _ := data.Get("term").Int64()
	voteGranted, _ := data.Get("vote_granted").Bool()
	vresp := VoteResponse{
		Term:        term,
		VoteGranted: voteGranted,
	}
	return vresp, nil
}

func (t *HTTPTransport) AppendEntriesRPC(address string, entryRequest EntryRequest) (EntryResponse, error) {
	endpoint := fmt.Sprintf("http://%s/append_entries", address)
	log.Printf("[%s] AppendEntriesRPC %+v to %s", t.node.ID, entryRequest, endpoint)
	_, err := apiRequest("POST", endpoint, entryRequest, 500*time.Millisecond)
	if err != nil {
		return EntryResponse{}, err
	}
	return EntryResponse{}, nil
}

func findLeaderIP(node *Node) (ip string) {
	findLeader := func(node *Node) (ip string) {
		ipchan := make(chan string)

		for _, p := range node.Cluster {

			fmt.Println(p.ID)

			go func(ip string) {
				resp, err := http.Get("http://" + ip + "/node")

				if err != nil {
					return
				}

				defer resp.Body.Close()

				body, err := ioutil.ReadAll(resp.Body)

				if err != nil {
					return
				}

				ss := string(body[:])

				fmt.Println(ss)

				if ss != "" {
					if ss[1] == byte('2') {
						ipchan <- ip
					}
				}

			}(p.ID)
		}

		select {
		case ipleader := <-ipchan:
			return ipleader
		case <-time.After(time.Second):
			return "notFound"
		}
	}

	if node.State == Leader {
		return node.Transport.(*HTTPTransport).Address
	}

	if node.VotedFor == "" {
		node.VotedFor = findLeader(node)
	}

	return node.VotedFor
}

func randomByteString(size int) []byte {
	b := make([]byte, size)

	for i := range b {
		b[i] = ASCII_CHARS[rand.Intn(len(ASCII_CHARS))]
	}

	return b
}
