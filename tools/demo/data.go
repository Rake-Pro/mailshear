package main

import (
	"time"

	"github.com/Rake-Pro/mailshear/internal/store"
)

// demoGroups is the synthetic mailbox the demo runs against: 25 sender
// groups over 19 domains, shaped like a real first run. Every address is on
// an .example domain, and none of it comes from a real mailbox.
//
// The shapes that matter for the screens are all here: one brand arriving
// under five List-Ids and subdomains, two mixed senders whose receipts sit
// beside their marketing, a protected bank, a sender that kept sending after
// an unsubscribe, and one already-decided sender that the review hides.
func demoGroups(now time.Time) []store.SenderGroup {
	day := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	hour := func(n int) time.Time { return now.Add(-time.Duration(n) * time.Hour) }
	// size approximates a mailbox at roughly 55 KB a message.
	size := func(count int) int64 { return int64(count) * 55 * 1024 }

	first := day(940)

	return []store.SenderGroup{
		// Protected: the bank is in the protected set the backend loads.
		{
			SenderKey: "addr:alerts@secure.bank.example", DomainKey: "bank.example",
			Display: "Example Bank Alerts", Address: "alerts@secure.bank.example",
			Count: 128, KeepCount: 61, KeepCategories: []string{"security", "statement"},
			TotalSize: size(128), FirstSeen: first, LastSeen: hour(9),
			Method: "none", RecentSubjects: []string{"Your statement is ready"},
			Folders: []string{"INBOX"},
		},

		// Keep: receipts and security mail mixed into bulk mail.
		{
			SenderKey: "addr:service@pay.example", DomainKey: "pay.example",
			Display: "PayPal", Address: "service@pay.example",
			Count: 412, MixedCount: 96, KeepCount: 88,
			KeepCategories: []string{"receipt", "payment", "security"},
			TotalSize:      size(412), FirstSeen: first, LastSeen: hour(5),
			Method: "http", LatestURIs: []string{"https://pay.example/u/casey"},
			RecentSubjects: []string{"You sent a payment", "Receipt from Northwind"},
			Folders:        []string{"INBOX", "Archive"},
		},
		{
			SenderKey: "addr:alerts@chase.example", DomainKey: "chase.example",
			Display: "Chase Alerts", Address: "alerts@chase.example",
			Count: 231, MixedCount: 64, KeepCount: 57,
			KeepCategories: []string{"statement", "security"},
			TotalSize:      size(231), FirstSeen: first, LastSeen: hour(11),
			Method: "none", RecentSubjects: []string{"Your statement is available"},
			Folders: []string{"INBOX"},
		},
		{
			SenderKey: "list:offers.chase.example", DomainKey: "chase.example",
			Display: "Chase Offers", Address: "offers@e.chase.example", ListID: "offers.chase.example",
			Count: 74, TotalSize: size(74), FirstSeen: day(610), LastSeen: day(2),
			Method: "http", LatestURIs: []string{"https://e.chase.example/unsub?t=9f2"},
			RecentSubjects: []string{"A card offer picked for you"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "addr:itinerary@mail.airline.example", DomainKey: "airline.example",
			Display: "Example Airlines", Address: "itinerary@mail.airline.example",
			Count: 96, MixedCount: 31, KeepCount: 24,
			KeepCategories: []string{"booking", "receipt"},
			TotalSize:      size(96), FirstSeen: day(720), LastSeen: day(4),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://mail.airline.example/u/1c8d"},
			RecentSubjects: []string{"Your trip to Portland"},
			Folders:        []string{"INBOX", "Archive"},
		},

		// Still sending: unsubscribed once, kept arriving.
		{
			SenderKey: "list:daily.dealsdaily.example", DomainKey: "dealsdaily.example",
			Display: "Deals Daily", Address: "news@dealsdaily.example", ListID: "daily.dealsdaily.example",
			Count: 187, TotalSize: size(187), FirstSeen: day(480), LastSeen: hour(2),
			Method: "http", LatestURIs: []string{"https://dealsdaily.example/optout"},
			RecentSubjects: []string{"Today only: 40% off"},
			Folders:        []string{"INBOX"},
			Decision:       "unsubscribe", DecidedAt: day(28),
			UnsubStatus: "ok", UnsubAt: day(28),
			StillSending: true, StillSendingCount: 23,
		},

		// Bulk: one brand under five senders, then the ordinary tail.
		{
			SenderKey: "list:jobs.linkedin.example", DomainKey: "linkedin.example",
			Display: "LinkedIn", Address: "jobs@e.linkedin.example", ListID: "jobs.linkedin.example",
			Count: 986, TotalSize: size(986), FirstSeen: first, LastSeen: hour(3),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://e.linkedin.example/u/jobs/8a1f"},
			RecentSubjects: []string{"9 jobs for you", "Your job alert for Go"},
			Folders:        []string{"INBOX", "Archive"},
		},
		{
			SenderKey: "list:news.linkedin.example", DomainKey: "linkedin.example",
			Display: "LinkedIn", Address: "news@e.linkedin.example", ListID: "news.linkedin.example",
			Count: 641, TotalSize: size(641), FirstSeen: first, LastSeen: hour(6),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://e.linkedin.example/u/news/44c2"},
			RecentSubjects: []string{"Your network in the news"},
			Folders:        []string{"INBOX", "Archive"},
		},
		{
			SenderKey: "list:groups.linkedin.example", DomainKey: "linkedin.example",
			Display: "LinkedIn", Address: "groups@g.linkedin.example", ListID: "groups.linkedin.example",
			Count: 402, TotalSize: size(402), FirstSeen: day(830), LastSeen: day(1),
			Method: "http", LatestURIs: []string{"https://g.linkedin.example/u/groups/71b0"},
			RecentSubjects: []string{"New posts in Gophers"},
			Folders:        []string{"Archive"},
		},
		{
			SenderKey: "addr:invitations@linkedin.example", DomainKey: "linkedin.example",
			Display: "LinkedIn Invitations", Address: "invitations@linkedin.example",
			Count: 268, TotalSize: size(268), FirstSeen: day(760), LastSeen: day(3),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://e.linkedin.example/u/inv/5d33"},
			RecentSubjects: []string{"You have 4 invitations waiting"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "list:learning.linkedin.example", DomainKey: "linkedin.example",
			Display: "LinkedIn Learning", Address: "learning@l.linkedin.example", ListID: "learning.linkedin.example",
			Count: 116, TotalSize: size(116), FirstSeen: day(540), LastSeen: day(6),
			Method: "none", RecentSubjects: []string{"Finish your course"},
			Folders: []string{"Archive"},
		},

		{
			SenderKey: "list:weekly.northwind.example", DomainKey: "northwind.example",
			Display: "Northwind Weekly", Address: "news@northwind.example", ListID: "weekly.northwind.example",
			Count: 743, RepliedCount: 1, TotalSize: size(743),
			FirstSeen: first, LastSeen: day(1),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://northwind.example/u/2f9a"},
			RecentSubjects: []string{"Issue 214: the long way round"},
			Folders:        []string{"INBOX", "Archive"},
		},
		{
			SenderKey: "addr:hello@craftcoffee.example", DomainKey: "craftcoffee.example",
			Display: "Craft Coffee Club", Address: "hello@craftcoffee.example",
			Count: 512, TotalSize: size(512), FirstSeen: day(880), LastSeen: hour(20),
			Method: "http", LatestURIs: []string{"https://craftcoffee.example/unsubscribe?id=b41"},
			RecentSubjects: []string{"This month's roast"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "list:deals.wanderlist.example", DomainKey: "wanderlist.example",
			Display: "Wanderlist Travel", Address: "deals@wanderlist.example", ListID: "deals.wanderlist.example",
			Count: 468, TotalSize: size(468), FirstSeen: day(700), LastSeen: day(2),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://wanderlist.example/u/e71c"},
			RecentSubjects: []string{"Fares under 200 this week"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "list:digest.devlog.example", DomainKey: "devlog.example",
			Display: "Devlog Digest", Address: "digest@devlog.example", ListID: "digest.devlog.example",
			Count: 431, TotalSize: size(431), FirstSeen: day(650), LastSeen: day(1),
			Method: "mailto", LatestURIs: []string{"mailto:unsubscribe@devlog.example?subject=stop"},
			RecentSubjects: []string{"Friday links"},
			Folders:        []string{"Archive"},
		},
		{
			SenderKey: "addr:news@pixelforge.example", DomainKey: "pixelforge.example",
			Display: "Pixel Forge Games", Address: "news@pixelforge.example",
			Count: 388, TotalSize: size(388), FirstSeen: day(520), LastSeen: day(3),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://pixelforge.example/u/9c2e"},
			RecentSubjects: []string{"Patch 4.2 is live"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "list:morning.dailybyte.example", DomainKey: "dailybyte.example",
			Display: "The Daily Byte", Address: "morning@dailybyte.example", ListID: "morning.dailybyte.example",
			Count: 352, TotalSize: size(352), FirstSeen: day(430), LastSeen: hour(14),
			Method: "http", LatestURIs: []string{"https://dailybyte.example/optout/7731"},
			RecentSubjects: []string{"Monday briefing"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "addr:offers@homestead.example", DomainKey: "homestead.example",
			Display: "Homestead Supply", Address: "offers@homestead.example",
			Count: 297, FlaggedCount: 2, TotalSize: size(297),
			FirstSeen: day(610), LastSeen: day(5),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://homestead.example/u/33af"},
			RecentSubjects: []string{"Spring seed catalogue"},
			Folders:        []string{"INBOX", "Archive"},
		},
		{
			SenderKey: "addr:news@trailhead.example", DomainKey: "trailhead.example",
			Display: "Trailhead Outdoors", Address: "news@trailhead.example",
			Count: 264, TotalSize: size(264), FirstSeen: day(590), LastSeen: day(8),
			Method: "none", RecentSubjects: []string{"Trail conditions this weekend"},
			Folders: []string{"INBOX"},
		},
		{
			SenderKey: "list:team.ferrousfitness.example", DomainKey: "ferrousfitness.example",
			Display: "Ferrous Fitness", Address: "team@ferrousfitness.example", ListID: "team.ferrousfitness.example",
			Count: 233, TotalSize: size(233), FirstSeen: day(500), LastSeen: day(4),
			Method: "http", LatestURIs: []string{"https://ferrousfitness.example/unsub/c19"},
			RecentSubjects: []string{"Week 6 of the program"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "addr:hello@brightlamp.example", DomainKey: "brightlamp.example",
			Display: "Bright Lamp Studio", Address: "hello@brightlamp.example",
			Count: 198, TotalSize: size(198), FirstSeen: day(470), LastSeen: day(9),
			Method: "mailto", LatestURIs: []string{"mailto:leave@brightlamp.example"},
			RecentSubjects: []string{"New prints in the shop"},
			Folders:        []string{"Archive"},
		},
		{
			SenderKey: "list:bulletin.civic.example", DomainKey: "civic.example",
			Display: "Civic Notices", Address: "bulletin@civic.example", ListID: "bulletin.civic.example",
			Count: 176, TotalSize: size(176), FirstSeen: day(910), LastSeen: day(7),
			Method: "oneclick", LatestOneClick: true,
			LatestURIs:     []string{"https://civic.example/u/bulletin"},
			RecentSubjects: []string{"Road works on Vine Street"},
			Folders:        []string{"INBOX"},
		},
		{
			SenderKey: "addr:newsletter@quarrybooks.example", DomainKey: "quarrybooks.example",
			Display: "Quarry Books", Address: "newsletter@quarrybooks.example",
			Count: 154, TotalSize: size(154), FirstSeen: day(420), LastSeen: day(11),
			Method: "http", LatestURIs: []string{"https://quarrybooks.example/u/ab21"},
			RecentSubjects: []string{"Out this month"},
			Folders:        []string{"Archive"},
		},
		{
			SenderKey: "addr:weekly@pinehill.example", DomainKey: "pinehill.example",
			Display: "Pinehill Grocery", Address: "weekly@pinehill.example",
			Count: 129, TotalSize: size(129), FirstSeen: day(380), LastSeen: day(6),
			Method: "http", LatestURIs: []string{"https://pinehill.example/unsub"},
			RecentSubjects: []string{"Weekly flyer"},
			Folders:        []string{"INBOX"},
		},

		// Already decided and long gone: the review hides it until h.
		{
			SenderKey: "addr:news@oldco.example", DomainKey: "oldco.example",
			Display: "Old Company News", Address: "news@oldco.example",
			Count: 61, TotalSize: size(61), FirstSeen: day(900), LastSeen: day(46),
			Method: "http", LatestURIs: []string{"https://oldco.example/u/x"},
			Folders:  []string{"Archive"},
			Decision: "unsubscribe,delete_matched", DecidedAt: day(45),
			UnsubStatus: "ok", UnsubAt: day(45),
		},
	}
}

// protectedKeys is the set the backend reports as protected in the database.
func protectedKeys() map[string]bool {
	return map[string]bool{"addr:alerts@secure.bank.example": true}
}

// demoStatus is the unsubscribe outcome the fake run reports for a sender,
// chosen so one run shows every status the results screen can render.
func demoStatus(senderKey string) (status, errText string) {
	switch senderKey {
	case "list:groups.linkedin.example":
		return "failed", "get https://g.linkedin.example/u/groups/71b0: dial tcp: lookup g.linkedin.example: no such host (blocked by DNS filter)"
	case "addr:service@pay.example":
		return "probable", ""
	case "list:learning.linkedin.example", "addr:alerts@chase.example",
		"addr:news@trailhead.example":
		return "manual", "no unsubscribe uri"
	}
	return "ok", ""
}
