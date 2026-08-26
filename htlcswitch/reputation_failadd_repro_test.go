package htlcswitch

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/stretchr/testify/require"
)

// TestReputationMailboxFailAddLosesOutgoingSCID reproduces the outgoing-SCID
// loss that occurs when the outgoing link fails an add through its mailbox
// (mailbox.FailAdd), i.e. when no keystone was ever set for the circuit.
//
// This is the path taken when channel.AddHTLC fails on the outgoing link
// (link.go:1656), when the link is flushing or quiesced (link.go:1599), when
// the fee exposure threshold is hit (link.go:1622), or when the mailbox
// delivery deadline elapses (mailbox.go:542).
//
// mailbox.FailAdd builds a *fresh* htlcPacket (mailbox.go:737) that does not
// carry outgoingChanID, and the circuit has no keystone, so the fallback in
// handlePacketFail (switch.go:3219) hands the reputation manager the zero SCID
// instead of the channel the HTLC was actually forwarded to.
func TestReputationMailboxFailAddLosesOutgoingSCID(t *testing.T) {
	t.Parallel()

	repMgr := &mockReputationManager{}
	s, aliceLink, bobLink := newReputationTestSwitch(t, repMgr)

	preimage, err := genPreimage()
	require.NoError(t, err)
	rhash := sha256.Sum256(preimage[:])

	addPkt := &htlcPacket{
		incomingChanID: aliceLink.ShortChanID(),
		incomingHTLCID: 0,
		outgoingChanID: bobLink.ShortChanID(),
		obfuscator:     NewMockObfuscator(),
		htlc: &lnwire.UpdateAddHTLC{
			PaymentHash: rhash,
			Amount:      1,
		},
	}
	require.NoError(t, s.ForwardPackets(nil, addPkt))

	// Take the packet out of the outgoing link's mailbox, but do NOT call
	// completeCircuit: that is what sets the keystone, and the whole point
	// of this path is that the add fails before a keystone exists.
	var queued *htlcPacket
	select {
	case queued = <-bobLink.packets:
	case <-time.After(time.Second):
		t.Fatal("add was not propagated to the destination link")
	}

	forwards, _, _ := repMgr.snapshot()
	require.Len(t, forwards, 1)
	t.Logf("OnForward outgoing scid = %v", forwards[0].out)

	// The outgoing link cannot add the HTLC to its commitment, so it fails
	// the add back through the mailbox.
	bobLink.mailBox.FailAdd(queued)

	select {
	case <-aliceLink.packets:
	case <-time.After(2 * time.Second):
		t.Fatal("fail was not propagated upstream")
	}

	require.Eventually(t, func() bool {
		_, _, fails := repMgr.snapshot()

		return len(fails) == 1
	}, 2*time.Second, 10*time.Millisecond)

	_, _, fails := repMgr.snapshot()
	t.Logf("OnFail    outgoing scid = %v (zero value is %v)", fails[0].out,
		lnwire.ShortChannelID{})

	require.Equal(t, forwards[0].out, fails[0].out,
		"OnFail must report the same outgoing channel as OnForward, "+
			"otherwise the manager cannot match the resolution to "+
			"the pending HTLC it recorded")
}
