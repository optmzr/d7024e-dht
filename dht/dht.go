package dht

import (
	"fmt"
	"log"

	"github.com/optmzr/d7024e-dht/network"
	"github.com/optmzr/d7024e-dht/node"
	"github.com/optmzr/d7024e-dht/route"
	"github.com/optmzr/d7024e-dht/store"
	"golang.org/x/crypto/blake2b"
)

const α = 3              // Degree of parallelism.
const k = 20             // Bucket size.
const tExpire = 86410    // Time after which a key/value pair expires (TTL).
const tReplicate = 3600  // Interval between Kademlia replication events.
const tRepublish = 86400 // Time after which the original publisher must republish a key/value pair.

type DHT struct {
	rt *route.Table
	nw network.Network
	me route.Contact
	db *store.Database
}

func New(me route.Contact, others []route.Contact, nw network.Network) (dht *DHT, err error) {
	dht = new(DHT)
	dht.rt, err = route.NewTable(me, others)
	if err != nil {
		err = fmt.Errorf("cannot initialize routing table: %w", err)
		return
	}

	dht.db = store.NewDatabase(tExpire, tReplicate, tRepublish)

	dht.nw = nw
	dht.me = me

	go func(dht *DHT, me route.Contact) {
		<-dht.nw.ReadyCh()
		err := dht.Join(me)
		if err != nil {
			log.Fatalln(err)
		}
	}(dht, me)

	go dht.findNodesRequestHandler()
	go dht.storeRequestHandler()

	return
}

func (dht *DHT) findNodesRequestHandler() {
	for {
		request := <-dht.nw.FindNodesRequestCh()

		log.Printf("Find node request from: %v", request.From.NodeID)

		// Add node so it is moved to the top of its bucket in the routing table.
		dht.rt.Add(request.From)

		// Fetch this nodes contacts that are closest to the requested target.
		closest := dht.rt.NClosest(request.Target, k).SortedContacts()

		err := dht.nw.SendNodes(closest, request.SessionID, request.From.Address)
		if err != nil {
			log.Println(err)
		}
	}
}

func (dht *DHT) storeRequestHandler() {
	for {
		request := <-dht.nw.StoreRequestCh()

		log.Printf("Store value request from: %v", request.From.NodeID)

		// Add node so it is moved to the top of its bucket in the routing table.
		dht.rt.Add(request.From)

		dht.db.AddItem(request.Value, request.From.NodeID)
	}
}

// Get retrieves the value for a specified key from the network.
func (dht *DHT) Get(hash store.Key) (value string, err error) {
	value, err = dht.iterativeFindValue(hash)
	return
}

// Put stores the provided value in the network and returns a key.
func (dht *DHT) Put(value string) (hash store.Key, err error) {
	hash, err = dht.iterativeStore(value)
	return
}

// Join initiates a node lookup of itself to bootstrap the node into the
// network.
func (dht *DHT) Join(me route.Contact) (err error) {
	contacts, err := dht.iterativeFindNodes(me.NodeID)
	if err != nil {
		return
	}

	logAcquaintedWith(contacts)
	return
}

type awaitChannel struct {
	ch     chan network.Result
	callee route.Contact
}

type awaitResult struct {
	result network.Result
	callee route.Contact
}

func (dht *DHT) walk(call Call) ([]route.Contact, error) {
	nw := dht.nw
	rt := dht.rt
	me := dht.me
	target := call.Target()

	// The first α contacts selected are used to create a *shortlist* for the
	// search.
	sl := dht.rt.NClosest(target, α)

	// Keep a map of contacts that has been sent to, to make sure we do not
	// contact the same node multiple times.
	sent := make(map[node.ID]bool)

	// If a cycle results in an unchanged `closest` node, then a FindNode
	// network call should be made to each of the closest nodes that has not
	// already been queried.
	rest := false

	// Contacts holds a sorted (slice) copy of the shortlist.
	contacts := sl.SortedContacts()

	// Closest is the node that closest in distance to the target node ID.
	closest := contacts[0]

	for {
		// Holds a slice of channels that are awaiting a response from the
		// network.
		await := []awaitChannel{}

		for i, contact := range contacts {
			if i >= α && !rest {
				break // Limit to α contacts per shortlist.
			}
			if sent[contact.NodeID] || contact.NodeID.Equal(me.NodeID) {
				continue // Ignore already contacted contacts or local node.
			}

			ch, err := call.Do(nw, contact.Address)
			if err != nil {
				sl.Remove(contact)
			} else {
				// Mark as contacted.
				sent[contact.NodeID] = true

				// Add to await channel queue.
				await = append(await, awaitChannel{ch: ch, callee: contact})
			}
		}

		results := make(chan awaitResult)
		for _, ac := range await {
			go func(ac awaitChannel) {
				// Redirect all responses to the results channel.
				r := <-ac.ch
				results <- awaitResult{result: r, callee: ac.callee}
			}(ac)
		}

		// Iterate through every result from the responding nodes and add their
		// closest contacts to the shortlist.
		for i := 0; i < len(await); i++ {
			ac := <-results
			result := ac.result
			callee := ac.callee

			if result != nil {
				// Add node so it is moved to the top of its bucket in the
				// routing table.
				rt.Add(callee)

				// Add the responding node's closest contacts.
				sl.Add(result.Closest()...)

				// Update callee with intermediate results.
				stop := call.Result(result)
				if stop {
					break // Callee requested that the walk must be stopped.
				}
			} else {
				// Network call timed out. Remove the callee from the shortlist.
				sl.Remove(callee)
			}
		}

		contacts = sl.SortedContacts()
		first := contacts[0]
		if closest.NodeID.Equal(first.NodeID) {
			// Unchanged closest node from last run, re-run but check all the
			// nodes in the shortlist (and not only the α closest).
			if !rest {
				rest = true
				continue
			}

			// Done. Return the contacts in the shortlist sorted by distance.
			return contacts, nil

		} else {
			// New closest node found, continue iteration.
			closest = first
		}
	}
}

func (dht *DHT) iterativeFindNodes(target node.ID) ([]route.Contact, error) {
	return dht.walk(NewFindNodesCall(target))
}

func (dht *DHT) iterativeStore(value string) (hash store.Key, err error) {
	hash = blake2b.Sum256([]byte(value))

	contacts, err := dht.iterativeFindNodes(node.ID(hash))
	if err != nil {
		return
	}

	var stored []route.Contact
	for _, contact := range contacts {
		if e := dht.nw.Store(hash, value, contact.Address); e != nil {
			log.Printf("Failed to store at %s (%s): %v",
				contact.NodeID.String(), contact.Address.String(), e)
		} else {
			stored = append(stored, contact)
		}
	}

	if len(stored) > 0 {
		logStoredAt(hash, stored)
	}

	return
}

func (dht *DHT) iterativeFindValue(hash store.Key) (value string, err error) {
	call := NewFindValueCall(hash)
	if _, err = dht.walk(call); err == nil {
		value = call.value
	}
	return
}

func logStoredAt(hash store.Key, contacts []route.Contact) {
	log.Printf("Stored value with hash %v at %d nodes:\n",
		hash.String(), len(contacts))
	logContacts(contacts)
}

func logAcquaintedWith(contacts []route.Contact) {
	log.Printf("Acquainted with %d nodes:\n", len(contacts))
	logContacts(contacts)
}

func logContacts(contacts []route.Contact) {
	for _, contact := range contacts {
		log.Println("\t", contact.NodeID.String())
	}
}
