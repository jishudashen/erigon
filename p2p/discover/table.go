// Copyright 2015 The go-ethereum Authors
// (original work)
// Copyright 2024 The Erigon Authors
// (modifications)
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

// Package discover implements the Node Discovery Protocol.
//
// The Node Discovery protocol provides a way to find RLPx nodes that
// can be connected to. It uses a Kademlia-like protocol to maintain a
// distributed database of the IDs and endpoints of all listening
// nodes.
package discover

import (
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erigontech/erigon-lib/common"
	"github.com/erigontech/erigon-lib/common/debug"
	"github.com/erigontech/erigon-lib/log/v3"
	"github.com/erigontech/erigon/p2p/enode"
	"github.com/erigontech/erigon/p2p/netutil"
)

const (
	alpha           = 3  // Kademlia concurrency factor
	bucketSize      = 16 // Kademlia bucket size
	maxReplacements = 10 // Size of per-bucket replacement list

	// We keep buckets for the upper 1/15 of distances because
	// it's very unlikely we'll ever encounter a node that's closer.
	hashBits          = len(common.Hash{}) * 8
	nBuckets          = hashBits / 15       // Number of buckets
	bucketMinDistance = hashBits - nBuckets // Log distance of closest bucket

	// IP address limits.
	bucketIPLimit, bucketSubnet = 2, 24 // at most 2 addresses from the same /24
	tableIPLimit, tableSubnet   = 10, 24

	minRefreshInterval  = 30 * time.Second
	refreshInterval     = 30 * time.Minute
	revalidateInterval  = 5 * time.Second
	maintenanceInterval = 60 * time.Second
	seedMinTableTime    = 5 * time.Minute
	seedCount           = 30
	seedMaxAge          = 5 * 24 * time.Hour
)

// Table is the 'node table', a Kademlia-like index of neighbor nodes. The table keeps
// itself up-to-date by verifying the liveness of neighbors and requesting their node
// records when announcements of a new record version are received.
type Table struct {
	mutex   sync.Mutex        // protects buckets, bucket content, nursery, rand
	buckets [nBuckets]*bucket // index of known nodes by distance
	nursery []*node           // bootstrap nodes
	rand    *mrand.Rand       // source of randomness, periodically reseeded
	ips     netutil.DistinctNetSet

	revalidateInterval time.Duration

	log        log.Logger
	db         *enode.DB // database of known nodes
	net        transport
	refreshReq chan chan struct{}
	initDone   chan struct{}
	closeReq   chan struct{}
	closed     chan struct{}

	nodeAddedHook func(*node) // for testing

	// diagnostics
	errors      map[string]uint
	dbseeds     atomic.Int32
	revalidates atomic.Int32
	protocol    string
}

// transport is implemented by the UDP transports.
type transport interface {
	Self() *enode.Node
	RequestENR(*enode.Node) (*enode.Node, error)
	lookupRandom() []*enode.Node
	lookupSelf() []*enode.Node
	ping(*enode.Node) (seq uint64, err error)
	Version() string
	Errors() map[string]uint
	LenUnsolicited() int
}

// bucket contains nodes, ordered by their last activity. the entry
// that was most recently active is the first element in entries.
type bucket struct {
	entries      []*node // live entries, sorted by time of last contact
	replacements []*node // recently seen nodes to be used if revalidation fails
	ips          netutil.DistinctNetSet
}

func newTable(
	t transport,
	protocol string,
	db *enode.DB,
	bootnodes []*enode.Node,
	revalidateInterval time.Duration,
	logger log.Logger,
) (*Table, error) {
	tab := &Table{
		net:                t,
		db:                 db,
		refreshReq:         make(chan chan struct{}),
		initDone:           make(chan struct{}),
		closeReq:           make(chan struct{}),
		closed:             make(chan struct{}),
		rand:               mrand.New(mrand.NewSource(0)), // nolint: gosec
		ips:                netutil.DistinctNetSet{Subnet: tableSubnet, Limit: tableIPLimit},
		errors:             map[string]uint{},
		revalidateInterval: revalidateInterval,
		protocol:           protocol,
		log:                logger,
	}
	if err := tab.setFallbackNodes(bootnodes); err != nil {
		return nil, err
	}
	for i := range tab.buckets {
		tab.buckets[i] = &bucket{
			ips: netutil.DistinctNetSet{Subnet: bucketSubnet, Limit: bucketIPLimit},
		}
	}
	tab.seedRand()
	tab.loadSeedNodes()

	return tab, nil
}

func (tab *Table) self() *enode.Node {
	return tab.net.Self()
}

func (tab *Table) seedRand() {
	var b [8]byte
	crand.Read(b[:])

	tab.mutex.Lock()
	defer tab.mutex.Unlock()
	tab.rand.Seed(int64(binary.BigEndian.Uint64(b[:])))
}

// ReadRandomNodes fills the given slice with random nodes from the table. The results
// are guaranteed to be unique for a single invocation, no node will appear twice.
func (tab *Table) ReadRandomNodes(buf []*enode.Node) (n int) {
	if !tab.isInitDone() {
		return 0
	}

	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	nodes := make([]*enode.Node, 0, len(&tab.buckets))
	for _, b := range &tab.buckets {
		for _, n := range b.entries {
			nodes = append(nodes, unwrapNode(n))
		}
	}
	// Shuffle.
	for i := 0; i < len(nodes); i++ {
		j := tab.rand.Intn(len(nodes))
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}
	return copy(buf, nodes)
}

// getNode returns the node with the given ID or nil if it isn't in the table.
func (tab *Table) getNode(id enode.ID) *enode.Node {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	b := tab.bucket(id)
	for _, e := range b.entries {
		if e.ID() == id {
			return unwrapNode(e)
		}
	}
	return nil
}

// close terminates the network listener and flushes the node database.
func (tab *Table) close() {
	close(tab.closeReq)
	<-tab.closed
}

// setFallbackNodes sets the initial points of contact. These nodes
// are used to connect to the network if the table is empty and there
// are no known nodes in the database.
func (tab *Table) setFallbackNodes(nodes []*enode.Node) error {
	for _, n := range nodes {
		if err := n.ValidateComplete(); err != nil {
			return fmt.Errorf("bad bootstrap node %q: %w", n, err)
		}
	}
	tab.nursery = wrapNodes(nodes)
	return nil
}

// isInitDone returns whether the table's initial seeding procedure has completed.
func (tab *Table) isInitDone() bool {
	select {
	case <-tab.initDone:
		return true
	default:
		return false
	}
}

func (tab *Table) refresh() <-chan struct{} {
	done := make(chan struct{})
	select {
	case tab.refreshReq <- done:
	case <-tab.closeReq:
		close(done)
	}
	return done
}

// loop schedules runs of doRefresh, doRevalidate and copyLiveNodes.
func (tab *Table) loop() {
	var (
		revalidate      = time.NewTimer(tab.revalidateInterval)
		refresh         = time.NewTicker(refreshInterval)
		tableMainenance = time.NewTicker(maintenanceInterval)
		refreshDone     = make(chan struct{})           // where doRefresh reports completion
		revalidateDone  chan struct{}                   // where doRevalidate reports completion
		waiting         = []chan struct{}{tab.initDone} // holds waiting callers while doRefresh runs
	)
	defer debug.LogPanic()
	defer refresh.Stop()
	defer revalidate.Stop()
	defer tableMainenance.Stop()

	// Start initial refresh.
	go tab.doRefresh(refreshDone)

	var minRefreshTimer *time.Timer
	var minRefreshTimerActive atomic.Bool

	defer func() {
		if minRefreshTimer != nil {
			minRefreshTimer.Stop()
		}
	}()

loop:
	for {
		select {
		case <-refresh.C:
			tab.seedRand()
			if refreshDone == nil {
				refreshDone = make(chan struct{})
				go tab.doRefresh(refreshDone)
			}
		case req := <-tab.refreshReq:
			waiting = append(waiting, req)
			if refreshDone == nil {
				refreshDone = make(chan struct{})
				go tab.doRefresh(refreshDone)
			}
		case <-refreshDone:
			for _, ch := range waiting {
				close(ch)
			}
			waiting, refreshDone = nil, nil
		case <-revalidate.C:
			if revalidateDone == nil {
				revalidateDone = make(chan struct{})
				go tab.doRevalidate(revalidateDone)
			}
		case <-revalidateDone:
			revalidate.Reset(tab.revalidateInterval)
			if tab.live() == 0 && len(waiting) == 0 && minRefreshTimerActive.CompareAndSwap(false, true) {
				minRefreshTimer = time.AfterFunc(minRefreshInterval, func() {
					defer minRefreshTimerActive.Store(false)
					tab.net.lookupRandom()
					tab.refresh()
				})
			}
			revalidateDone = nil
		case <-tableMainenance.C:
			live := tab.live()

			vals := []interface{}{"protocol", tab.protocol, "version", tab.net.Version(),
				"len", tab.len(), "live", tab.live(), "unsol", tab.net.LenUnsolicited(), "ips", tab.ips.Len(), "db", tab.dbseeds.Load(), "reval", tab.revalidates.Load()}

			func() {
				tab.mutex.Lock()
				defer tab.mutex.Unlock()

				for err, count := range tab.errors {
					vals = append(vals, err, count)
				}

				for err, count := range tab.net.Errors() {
					vals = append(vals, err, count)
				}
			}()

			tab.log.Debug("[p2p] Discovery table", vals...)

			if live != 0 {
				if revalidateDone == nil {
					revalidateDone = make(chan struct{})
					go tab.doRevalidate(revalidateDone)
				}
			} else {
				go tab.copyLiveNodes()
			}
		case <-tab.closeReq:
			break loop
		}
	}

	if refreshDone != nil {
		<-refreshDone
	}
	for _, ch := range waiting {
		close(ch)
	}
	if revalidateDone != nil {
		<-revalidateDone
	}
	close(tab.closed)
}

// doRefresh performs a lookup for a random target to keep buckets full. seed nodes are
// inserted if the table is empty (initial bootstrap or discarded faulty peers).
func (tab *Table) doRefresh(done chan struct{}) {
	defer debug.LogPanic()
	defer close(done)

	// Load nodes from the database and insert
	// them. This should yield a few previously seen nodes that are
	// (hopefully) still alive.
	tab.loadSeedNodes()

	// Run self lookup to discover new neighbor nodes.
	tab.net.lookupSelf()

	// The Kademlia paper specifies that the bucket refresh should
	// perform a lookup in the least recently used bucket. We cannot
	// adhere to this because the findnode target is a 512bit value
	// (not hash-sized) and it is not easily possible to generate a
	// sha3 preimage that falls into a chosen bucket.
	// We perform a few lookups with a random target instead.
	for i := 0; i < 3; i++ {
		tab.net.lookupRandom()
	}
}

func (tab *Table) loadSeedNodes() {
	dbseeds := tab.db.QuerySeeds(seedCount, seedMaxAge)
	tab.dbseeds.Store(int32(len(dbseeds)))

	seeds := wrapNodes(dbseeds)
	tab.log.Debug("QuerySeeds read nodes from the node DB", "count", len(seeds))
	seeds = append(seeds, tab.nursery...)
	for i := range seeds {
		seed := seeds[i]
		age := log.Lazy{Fn: func() interface{} { return time.Since(tab.db.LastPongReceived(seed.ID(), seed.IP())) }}
		tab.log.Trace("Found seed node in database", "id", seed.ID(), "addr", seed.addr(), "age", age)
		tab.addSeenNode(seed)
	}
}

// doRevalidate checks that the last node in a random bucket is still live and replaces or
// deletes the node if it isn't.
func (tab *Table) doRevalidate(done chan<- struct{}) {
	defer debug.LogPanic()
	defer func() { done <- struct{}{} }()

	tab.revalidates.Add(1)

	last, bi := tab.nodeToRevalidate()
	if last == nil {
		// No non-empty bucket found.
		return
	}

	// Ping the selected node and wait for a pong.
	remoteSeq, rErr := tab.net.ping(unwrapNode(last))

	// Also fetch record if the node replied and returned a higher sequence number.
	if rErr == nil {
		if last.Seq() < remoteSeq {
			if n, err := tab.net.RequestENR(unwrapNode(last)); err != nil {
				rErr = err
				tab.log.Trace("ENR request failed", "id", last.ID(), "addr", last.addr(), "err", err)
			} else {
				last = &node{Node: *n, addedAt: last.addedAt, livenessChecks: last.livenessChecks}
			}
		}
	}

	tab.mutex.Lock()
	defer tab.mutex.Unlock()
	b := tab.buckets[bi]
	if rErr == nil {
		// The node responded, move it to the front.
		last.livenessChecks++
		tab.log.Trace("Revalidated node", "b", bi, "id", last.ID(), "checks", last.livenessChecks)
		tab.bumpInBucket(b, last)
		return
	} else {
		tab.addError(rErr)
	}

	// No reply received, pick a replacement or delete the node if there aren't
	// any replacements.
	if r := tab.replace(b, last); r != nil {
		tab.log.Trace("Replaced dead node", "b", bi, "id", last.ID(), "ip", last.IP(), "checks", last.livenessChecks, "r", r.ID(), "rip", r.IP())
	} else {
		tab.log.Trace("Removed dead node", "b", bi, "id", last.ID(), "ip", last.IP(), "checks", last.livenessChecks)
	}
}

// nodeToRevalidate returns the last node in a random, non-empty bucket.
func (tab *Table) nodeToRevalidate() (n *node, bi int) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	for _, bi = range tab.rand.Perm(len(tab.buckets)) {
		b := tab.buckets[bi]
		if len(b.entries) > 0 {
			last := b.entries[len(b.entries)-1]
			return last, bi
		}
	}
	return nil, 0
}

// copyLiveNodes adds nodes from the table to the database if they have been in the table
// longer than seedMinTableTime.
func (tab *Table) copyLiveNodes() {
	tab.mutex.Lock()
	defer debug.LogPanic()
	defer tab.mutex.Unlock()

	now := time.Now()
	for _, b := range &tab.buckets {
		for _, n := range b.entries {
			if n.livenessChecks > 0 && now.Sub(n.addedAt) >= seedMinTableTime {
				tab.db.UpdateNode(unwrapNode(n))
			}
		}
	}
}

// findnodeByID returns the n nodes in the table that are closest to the given id.
// This is used by the FINDNODE/v4 handler.
//
// The preferLive parameter says whether the caller wants liveness-checked results. If
// preferLive is true and the table contains any verified nodes, the result will not
// contain unverified nodes. However, if there are no verified nodes at all, the result
// will contain unverified nodes.
func (tab *Table) findnodeByID(target enode.ID, nresults int, preferLive bool) *nodesByDistance {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	// Scan all buckets. There might be a better way to do this, but there aren't that many
	// buckets, so this solution should be fine. The worst-case complexity of this loop
	// is O(tab.len() * nresults).
	nodes := &nodesByDistance{target: target}
	liveNodes := &nodesByDistance{target: target}
	for _, b := range &tab.buckets {
		for _, n := range b.entries {
			nodes.push(n, nresults)
			if preferLive && n.livenessChecks > 0 {
				liveNodes.push(n, nresults)
			}
		}
	}

	if preferLive && len(liveNodes.entries) > 0 {
		return liveNodes
	}
	return nodes
}

// len returns the number of nodes in the table.
func (tab *Table) len() (n int) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	for _, b := range &tab.buckets {
		n += len(b.entries)
	}
	return n
}

func (tab *Table) live() (n int) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	for _, b := range &tab.buckets {
		for _, e := range b.entries {
			if e.livenessChecks > 0 {
				n++
			}
		}
	}

	return n
}

func (tab *Table) addError(err error) {
	str := err.Error()
	tab.errors[str] = tab.errors[str] + 1
}

// bucketLen returns the number of nodes in the bucket for the given ID.
func (tab *Table) bucketLen(id enode.ID) int {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	return len(tab.bucket(id).entries)
}

// bucket returns the bucket for the given node ID hash.
func (tab *Table) bucket(id enode.ID) *bucket {
	d := enode.LogDist(tab.self().ID(), id)
	return tab.bucketAtDistance(d)
}

func (tab *Table) bucketAtDistance(d int) *bucket {
	if d <= bucketMinDistance {
		return tab.buckets[0]
	}
	return tab.buckets[d-bucketMinDistance-1]
}

// addSeenNode adds a node which may or may not be live to the end of a bucket. If the
// bucket has space available, adding the node succeeds immediately. Otherwise, the node is
// added to the replacements list.
//
// The caller must not hold tab.mutex.
func (tab *Table) addSeenNode(n *node) {
	if n.ID() == tab.self().ID() {
		return
	}

	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	b := tab.bucket(n.ID())
	if contains(b.entries, n.ID()) {
		// Already in bucket, don't add.
		return
	}
	if len(b.entries) >= bucketSize {
		// Bucket full, maybe add as replacement.
		tab.addReplacement(b, n)
		return
	}
	if !tab.addIP(b, n.IP()) {
		// Can't add: IP limit reached.
		return
	}
	// Add to end of bucket:
	b.entries = append(b.entries, n)
	b.replacements = deleteNode(b.replacements, n)
	n.addedAt = time.Now()
	if tab.nodeAddedHook != nil {
		tab.nodeAddedHook(n)
	}
}

// addVerifiedNode adds a node whose existence has been verified recently to the front of a
// bucket. If the node is already in the bucket, it is moved to the front. If the bucket
// has no space, the node is added to the replacements list.
//
// There is an additional safety measure: if the table is still initializing the node
// is not added. This prevents an attack where the table could be filled by just sending
// ping repeatedly.
//
// The caller must not hold tab.mutex.
func (tab *Table) addVerifiedNode(n *node) {
	if !tab.isInitDone() {
		return
	}
	if n.ID() == tab.self().ID() {
		return
	}

	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	b := tab.bucket(n.ID())
	if tab.bumpInBucket(b, n) {
		// Already in bucket, moved to front.
		return
	}
	if len(b.entries) >= bucketSize {
		// Bucket full, maybe add as replacement.
		tab.addReplacement(b, n)
		return
	}
	if !tab.addIP(b, n.IP()) {
		// Can't add: IP limit reached.
		return
	}
	// Add to front of bucket.
	b.entries, _ = pushNode(b.entries, n, bucketSize)
	b.replacements = deleteNode(b.replacements, n)
	n.addedAt = time.Now()
	if tab.nodeAddedHook != nil {
		tab.nodeAddedHook(n)
	}
}

// delete removes an entry from the node table. It is used to evacuate dead nodes.
func (tab *Table) delete(node *node) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	tab.deleteInBucket(tab.bucket(node.ID()), node)
}

func (tab *Table) addIP(b *bucket, ip net.IP) bool {
	if len(ip) == 0 {
		return false // Nodes without IP cannot be added.
	}
	if netutil.IsLAN(ip) {
		return true
	}
	if !tab.ips.Add(ip) {
		tab.log.Trace("IP exceeds table limit", "ip", ip)
		return false
	}
	if !b.ips.Add(ip) {
		tab.log.Trace("IP exceeds bucket limit", "ip", ip)
		tab.ips.Remove(ip)
		return false
	}
	return true
}

func (tab *Table) removeIP(b *bucket, ip net.IP) {
	if netutil.IsLAN(ip) {
		return
	}
	tab.ips.Remove(ip)
	b.ips.Remove(ip)
}

func (tab *Table) addReplacement(b *bucket, n *node) {
	for _, e := range b.replacements {
		if e.ID() == n.ID() {
			return // already in list
		}
	}
	if !tab.addIP(b, n.IP()) {
		return
	}
	var removed *node
	b.replacements, removed = pushNode(b.replacements, n, maxReplacements)
	if removed != nil {
		tab.removeIP(b, removed.IP())
	}
}

// replace removes n from the replacement list and replaces 'last' with it if it is the
// last entry in the bucket. If 'last' isn't the last entry, it has either been replaced
// with someone else or became active.
func (tab *Table) replace(b *bucket, last *node) *node {
	if len(b.entries) == 0 || b.entries[len(b.entries)-1].ID() != last.ID() {
		// Entry has moved, don't replace it.
		return nil
	}
	// Still the last entry.
	if len(b.replacements) == 0 {
		tab.deleteInBucket(b, last)
		return nil
	}
	r := b.replacements[tab.rand.Intn(len(b.replacements))]
	b.replacements = deleteNode(b.replacements, r)
	b.entries[len(b.entries)-1] = r
	tab.removeIP(b, last.IP())
	return r
}

// bumpInBucket moves the given node to the front of the bucket entry list
// if it is contained in that list.
func (tab *Table) bumpInBucket(b *bucket, n *node) bool {
	for i := range b.entries {
		if b.entries[i].ID() == n.ID() {
			if !n.IP().Equal(b.entries[i].IP()) {
				// Endpoint has changed, ensure that the new IP fits into table limits.
				tab.removeIP(b, b.entries[i].IP())
				if !tab.addIP(b, n.IP()) {
					// It doesn't, put the previous one back.
					tab.addIP(b, b.entries[i].IP())
					return false
				}
			}
			// Move it to the front.
			copy(b.entries[1:], b.entries[:i])
			b.entries[0] = n
			return true
		}
	}
	return false
}

func (tab *Table) deleteInBucket(b *bucket, n *node) {
	b.entries = deleteNode(b.entries, n)
	tab.removeIP(b, n.IP())
}

func contains(ns []*node, id enode.ID) bool {
	for _, n := range ns {
		if n.ID() == id {
			return true
		}
	}
	return false
}

// pushNode adds n to the front of list, keeping at most max items.
func pushNode(list []*node, n *node, _max int) ([]*node, *node) {
	if len(list) < _max {
		list = append(list, nil)
	}
	removed := list[len(list)-1]
	copy(list[1:], list)
	list[0] = n
	return list, removed
}

// deleteNode removes n from list.
func deleteNode(list []*node, n *node) []*node {
	for i := range list {
		if list[i].ID() == n.ID() {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// nodesByDistance is a list of nodes, ordered by distance to target.
type nodesByDistance struct {
	entries []*node
	target  enode.ID
}

// push adds the given node to the list, keeping the total size below maxElems.
func (h *nodesByDistance) push(n *node, maxElems int) {
	ix := sort.Search(len(h.entries), func(i int) bool {
		return enode.DistCmp(h.target, h.entries[i].ID(), n.ID()) > 0
	})
	if len(h.entries) < maxElems {
		h.entries = append(h.entries, n)
	}
	if ix == len(h.entries) {
		// farther away than all nodes we already have.
		// if there was room for it, the node is now the last element.
	} else {
		// slide existing entries down to make room
		// this will overwrite the entry we just appended.
		copy(h.entries[ix+1:], h.entries[ix:])
		h.entries[ix] = n
	}
}
