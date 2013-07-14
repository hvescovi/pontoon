package pontoon

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	Follower = iota
	Candidate
	Leader
)

type Node struct {
	sync.RWMutex

	ID       string
	State    int
	Term     int64
	VotedFor string
	Log      *Log
	Votes    int
	Cluster  []string

	httpListener net.Listener

	exitChan         chan int
	voteResponseChan chan int
	termDiscoverChan chan int64

	requestVoteChan         chan VoteRequest
	requestVoteResponseChan chan VoteResponse

	appendEntriesChan         chan EntryRequest
	appendEntriesResponseChan chan EntryResponse

	endElectionChan      chan int
	finishedElectionChan chan int
}

func NewNode(id string) *Node {
	node := &Node{
		ID:  id,
		Log: &Log{},

		exitChan:         make(chan int),
		voteResponseChan: make(chan int),
		termDiscoverChan: make(chan int64),

		requestVoteChan:         make(chan VoteRequest),
		requestVoteResponseChan: make(chan VoteResponse),

		appendEntriesChan:         make(chan EntryRequest),
		appendEntriesResponseChan: make(chan EntryResponse),
	}
	go node.StateMachine()
	return node
}

func (n *Node) Exit() error {
	return n.httpListener.Close()
}

func (n *Node) AddToCluster(member string) {
	n.Cluster = append(n.Cluster, member)
}

func (n *Node) Serve(address string) {
	httpListener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("FATAL: listen (%s) failed - %s", address, err.Error())
	}
	n.httpListener = httpListener

	server := &http.Server{
		Handler: n,
	}
	go func() {
		log.Printf("[%s] starting HTTP server", n.ID)
		err := server.Serve(httpListener)
		// theres no direct way to detect this error because it is not exposed
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("ERROR: http.Serve() - %s", err.Error())
		}

		close(n.exitChan)
		log.Printf("exiting Serve()")
	}()
}

func (n *Node) StateMachine() {
	log.Printf("[%s] starting StateMachine()", n.ID)

	electionTimeout := 500 * time.Millisecond
	electionTimer := time.NewTimer(electionTimeout)
	heartbeatInterval := 100 * time.Millisecond
	heartbeatTimer := time.NewTicker(heartbeatInterval)

	for {
		log.Printf("[%s] StateMachine() loop", n.ID)

		select {
		case <-n.exitChan:
			goto exit
		case newTerm := <-n.termDiscoverChan:
			n.SetTerm(newTerm)
			continue
		case vreq := <-n.requestVoteChan:
			vresp, _ := n.doRequestVote(vreq)
			n.requestVoteResponseChan <- vresp
		case ereq := <-n.appendEntriesChan:
			eresp, _ := n.doAppendEntries(ereq)
			n.appendEntriesResponseChan <- eresp
		case <-electionTimer.C:
			n.ElectionTimeout()
		case <-n.voteResponseChan:
			n.VoteGranted()
		case <-heartbeatTimer.C:
			n.SendHeartbeat()
			continue
		}

		if !electionTimer.Reset(electionTimeout) {
			electionTimer = time.NewTimer(electionTimeout)
		}
	}

exit:
	log.Printf("[%s] starting StateMachine()", n.ID)
}

func (n *Node) SetTerm(term int64) {
	n.Lock()
	defer n.Unlock()

	if n.State == Candidate && n.endElectionChan != nil {
		// we discovered a new term in the current election, end it
		n.EndElection()
	}

	// check freshness
	if term <= n.Term {
		return
	}

	n.Term = term
	n.StepDown()
}

func (n *Node) NextTerm() {
	n.Lock()
	defer n.Unlock()
	n.Term++
	n.StepDown()
}

func (n *Node) StepDown() {
	log.Printf("[%s] StepDown()", n.ID)
	n.State = Follower
	n.VotedFor = ""
	n.Votes = 0
}

func (n *Node) PromoteToLeader() {
	n.Lock()
	defer n.Unlock()
	log.Printf("[%s] PromoteToLeader()", n.ID)
	n.State = Leader
}

func (n *Node) ElectionTimeout() {
	log.Printf("[%s] ElectionTimeout()", n.ID)
	n.NextTerm()
	n.RunForLeader()
}

func (n *Node) VoteGranted() {
	n.Lock()
	n.Votes++
	votes := n.Votes
	majority := (len(n.Cluster)+1)/2 + 1
	n.Unlock()

	log.Printf("[%s] VoteGranted() %d >= %d", n.ID, votes, majority)

	if votes >= majority {
		// we won election, end it and promote
		n.EndElection()
		n.PromoteToLeader()
	}
}

func (n *Node) EndElection() {
	log.Printf("[%s] EndElection()", n.ID)
	close(n.endElectionChan)
	<-n.finishedElectionChan
	n.endElectionChan = nil
	n.finishedElectionChan = nil
}

// - Increment currentTerm, vote for self
// - Reset election timeout
// - Send RequestVote RPCs to all other servers, wait for either:
//   - Votes received from majority of servers: become leader
//   - AppendEntries RPC received from new leader: step down
//   - Election timeout elapses without election resolution: increment term, start new election
//   - Discover higher term: step down
func (n *Node) RunForLeader() {
	log.Printf("[%s] RunForLeader()", n.ID)

	n.Lock()
	n.State = Candidate
	n.Votes++
	electionTerm := n.Term
	n.endElectionChan = make(chan int)
	n.finishedElectionChan = make(chan int)
	n.Unlock()

	go func() {
		for {
			voteResponseChan := make(chan *VoteResponse, len(n.Cluster))
			for _, peer := range n.Cluster {
				go func(p string) {
					voteResponseChan <- n.SendVoteRequest(p)
				}(peer)
			}

			for {
				select {
				case resp := <-voteResponseChan:
					if resp == nil {
						// TODO: should be retrying these
						continue
					}
					if resp.Term != electionTerm {
						if resp.Term > electionTerm {
							// we discovered a higher term
							n.termDiscoverChan <- resp.Term
							continue
						}
						continue
					}
					if resp.VoteGranted {
						n.voteResponseChan <- 1
					}
				case <-n.endElectionChan:
					close(n.finishedElectionChan)
					return
				}
			}
		}
	}()
}

func (n *Node) SendVoteRequest(peer string) *VoteResponse {
	endpoint := fmt.Sprintf("http://%s/request_vote", peer)
	vr := VoteRequest{
		Term:         n.Term,
		CandidateID:  n.httpListener.Addr().String(),
		LastLogIndex: n.Log.Index,
		LastLogTerm:  n.Log.Term,
	}
	log.Printf("[%s] VoteRequest %+v to %s", n.ID, vr, endpoint)
	data, err := ApiRequest("POST", endpoint, vr, 100*time.Millisecond)
	if err != nil {
		log.Printf("ERROR: %s - %s", endpoint, err.Error())
		return nil
	}
	term, _ := data.Get("term").Int64()
	voteGranted, _ := data.Get("vote_granted").Bool()
	return &VoteResponse{
		Term:        term,
		VoteGranted: voteGranted,
	}
}

func (n *Node) doRequestVote(vr VoteRequest) (VoteResponse, error) {
	log.Printf("[%s] doRequestVote() %+v", n.ID, vr)

	if vr.Term < n.Term {
		return VoteResponse{n.Term, false}, nil
	}

	if vr.Term > n.Term {
		n.SetTerm(vr.Term)
		return VoteResponse{n.Term, false}, nil
	}

	if n.VotedFor != "" && n.VotedFor != vr.CandidateID {
		return VoteResponse{n.Term, false}, nil
	}

	// TODO: check log
	log.Printf("[%s] granting vote to %s", n.ID, vr.CandidateID)
	n.VotedFor = vr.CandidateID
	return VoteResponse{n.Term, true}, nil
}

func (n *Node) RequestVote(vr VoteRequest) (VoteResponse, error) {
	n.requestVoteChan <- vr
	return <-n.requestVoteResponseChan, nil
}

func (n *Node) doAppendEntries(er EntryRequest) (EntryResponse, error) {
	// TODO: check if we're a candidate and end the election (someone else became leader)
	if n.State != Follower {
		n.StepDown()
	}
	return EntryResponse{}, nil
}

func (n *Node) AppendEntries(er EntryRequest) (EntryResponse, error) {
	n.appendEntriesChan <- er
	return <-n.appendEntriesResponseChan, nil
}

func (n *Node) SendHeartbeat() {
	n.RLock()
	state := n.State
	n.RUnlock()

	log.Printf("[%s] SendHeartbeat()", n.ID)

	if state == Leader {
		for _, peer := range n.Cluster {
			go func(p string) {
				endpoint := fmt.Sprintf("http://%s/append_entries", p)
				er := EntryRequest{}
				log.Printf("[%s] AppendEntries %+v to %s", n.ID, er, endpoint)
				_, err := ApiRequest("POST", endpoint, er, 500*time.Millisecond)
				if err != nil {
					log.Printf("ERROR: %s - %s", endpoint, err.Error())
					return
				}
			}(peer)
		}
	}
}
