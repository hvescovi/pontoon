package pontoon

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HTTPTransport struct {
	Address  string
	node     *Node
	listener net.Listener
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
	// if req.Method != "POST" {
	// 	apiResponse(w, 405, "MUST USE HTTP POST")
	// }

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
		// fmt.Fprint(w, t.node.Log.PrintAll())
		apiResponse(w, 299, t.node.Log.PrintAll())
	case "/cluster":
		var str string
		for _, peer := range t.node.Cluster {
			str += peer.ID + " " // + strconv.FormatInt(peer.NextIndex, 10) + " "
		}
		apiResponse(w, 299, str)

	case "/node":
		apiResponse(w, 299, strconv.Itoa(t.node.State))

	case "/leader":
		apiResponse(w, 299, findLeaderIP(t.node))

	default:

		fmt.Fprintln(w, req.URL.Path)

		command := strings.Split(req.URL.Path[1:], "/")

		fmt.Fprintln(w, command)

		if command[0] == "command" {
			if t.node.State == Leader {
				respChan := make(chan CommandResponse, 1)

				id, _ := strconv.Atoi(command[1])

				body := []byte(command[2])

				cr := CommandRequest{
					ID:           int64(id),
					Name:         "SUP",
					Body:         body,
					ResponseChan: respChan,
				}

				t.node.Command(cr)

				if (<-respChan).Success {
					apiResponse(w, 299, "Sucesso!")
				} else {
					apiResponse(w, 403, "Erro no request")
				}

			} else {
				redIP := "http://" + findLeaderIP(t.node) + req.URL.Path

				fmt.Fprintln(w, "redirecting to "+redIP)

				http.Redirect(w, req, redIP, 301)
			}

		} else {
			apiResponse(w, 403, "Erro no comando")
		}

	}
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

	if node.State == Leader {
		return node.Transport.String()
	}

	ipchan := make(chan string)

	for _, p := range node.Cluster {
		fmt.Println(p.ID)
		go func(ip string) {
			fmt.Println(ip)
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

				fmt.Println("resp not empty: " + ip)

				if ss[1] == byte('2') {
					// fmt.Println(ip + PORT)
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

// else {

// 	for _, peer := range node.Cluster {

// 		ip := peer.ID

// 		go func(ip string) {

// 			for _, port := range ValidPorts {

// 				if port == myport && ip == node.tra {
// 					continue
// 				}

// 				if find(ip+port, ipsAdded) {
// 					continue
// 				}

// 				go func(ip string, port string, m *sync.Mutex) {
// 					resp, err := http.Get("http://" + ip + port + "/ping")

// 					if err != nil {
// 						return
// 					}

// 					defer resp.Body.Close()

// 					body, err := ioutil.ReadAll(resp.Body)

// 					if err != nil {
// 						return
// 					}

// 					ss := string(body[:])

// 					fmt.Println(ss)

// 					if ss != "" {
// 						ipsAdded = append(ipsAdded, ip+port)
// 						node.AddToCluster(ip + port)

// 					}

// 				}(ip, port)

// 			}

// 		}(remoteip)

// 	}
// }
