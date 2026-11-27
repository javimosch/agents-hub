package hub

import (
	"fmt"
	"net/url"

	"github.com/javimosch/agents-hub/internal/bkn"
)

// historyPage is how many records one request asks for when all are wanted.
const historyPage = 200

// listAll reads every record matching q. bkn answers a list that names no
// limit with its first 50, so "no limit" has to be spelled as paging.
func (h *Hub) listAll(ref string, q url.Values) ([]bkn.Record, error) {
	page := url.Values{}
	for k, v := range q {
		page[k] = v
	}
	page.Set("limit", fmt.Sprint(historyPage))
	var all []bkn.Record
	for {
		page.Set("offset", fmt.Sprint(len(all)))
		recs, err := h.C.List(ref, page)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
		if len(recs) < historyPage {
			return all, nil
		}
	}
}
