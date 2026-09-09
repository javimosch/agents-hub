// Package hub is the domain: assistants, threads, messages, and the chat turn
// that ties them together. Everything it persists lives in bkn collections.
package hub

import (
	"time"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// Collection refs. One namespace, three collections -- the whole data model.
const (
	NS             = "hub"
	RefAssistants  = NS + "/assistants"
	RefThreads     = NS + "/threads"
	RefMessages    = NS + "/messages"
	OwnerFieldName = "user_id"
)

// Refs lists every collection the hub owns, in declare order.
func Refs() []string { return []string{RefAssistants, RefThreads, RefMessages} }

// Hub is the domain bound to one bkn client (and therefore one identity).
type Hub struct{ C *bkn.Client }

// New binds the domain to a client.
func New(c *bkn.Client) *Hub { return &Hub{C: c} }

// now is the single timestamp format used across every stored document.
// RFC3339 sorts lexicographically, which is what makes order_by=created_at
// work against a store that does not know these are dates.
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ownerPolicy is the access declaration for every hub collection.
//
// Tenancy is declared here once and enforced by bkn on every read and write:
// a create is stamped with the caller's id from their token, and a read is
// filtered to the caller's own documents. There is no code path in agents-hub
// that can widen it, because agents-hub never sends the owner field at all.
func ownerPolicy() map[string]any {
	return map[string]any{
		"access": map[string]any{
			"owner_field": OwnerFieldName,
			"rules": map[string]any{
				"read": "owner", "create": "owner",
				"update": "owner", "delete": "owner",
			},
		},
	}
}

// Setup declares the collections and their policy. It takes its own client
// because it is the only operation that needs the admin token: a process that
// can rewrite its own authorization rules at startup is a process whose rules
// mean less, so this is a separate command a human runs once, not something
// the tool does on the way to answering a chat.
func Setup(admin *bkn.Client) ([]string, error) {
	done := make([]string, 0, len(Refs()))
	for _, ref := range Refs() {
		if err := admin.Declare(ref, ownerPolicy()); err != nil {
			return done, err
		}
		done = append(done, ref)
	}
	return done, nil
}
