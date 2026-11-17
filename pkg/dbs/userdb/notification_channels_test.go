package userdb

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

var channelsUserSeq int64

// seedChannelsUser stores a user with a verified e-mail, a verified phone and the given stored
// channels, and returns its id.
func seedChannelsUser(t *testing.T, accountID string, phone string, channels []string) string {
	t.Helper()
	accountID = fmt.Sprintf("%d_%s", atomic.AddInt64(&channelsUserSeq, 1), accountID)
	user := models.User{
		Account: models.Account{Type: "email", AccountID: accountID, Password: "testhashedpassword-youcantreadme"},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: time.Now().Unix()},
		},
		ContactPreferences: models.ContactPreferences{PreferredChannels: channels},
		Timestamps:         models.Timestamps{CreatedAt: time.Now().Unix()},
	}
	id, err := testDBService.AddUser(testInstanceID, user)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return id
}

func storedChannels(t *testing.T, id string) []string {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, id)
	if err != nil {
		t.Fatalf("failed to read user: %v", err)
	}
	return user.ContactPreferences.PreferredChannels
}

func requireOnlyEmail(t *testing.T, got []string) {
	t.Helper()
	if len(got) != 1 || got[0] != models.ChannelEmail {
		t.Errorf("expected exactly [email], got %v", got)
	}
}

// requireNoChannel checks the "no channel" state: an empty list, stored as an empty array.
// Neither a missing field nor null will do, since both read as the legacy e-mail default.
func requireNoChannel(t *testing.T, id string) {
	t.Helper()
	if kind := rawChannelsType(t, id); kind != bsontype.Array {
		t.Fatalf("the channel list must be stored as an array, got BSON type %v", kind)
	}
	if got := storedChannels(t, id); got == nil || len(got) != 0 {
		t.Errorf("expected no channel (an empty, non-nil list), got %#v", got)
	}
}

// rawChannelsType is the BSON type preferredChannels is stored with; 0 means the field is missing.
func rawChannelsType(t *testing.T, id string) bsontype.Type {
	t.Helper()
	_id, _ := primitive.ObjectIDFromHex(id)
	ctx, cancel := testDBService.getContext()
	defer cancel()
	raw, err := testDBService.collectionRefUsers(testInstanceID).FindOne(ctx, bson.M{"_id": _id}).DecodeBytes()
	if err != nil {
		t.Fatalf("failed to read user: %v", err)
	}
	return raw.Lookup("contactPreferences", "preferredChannels").Type
}

// legacyChannelStates are the two shapes an account written before channels existed can have.
// Both read as e-mail.
var legacyChannelStates = map[string]bson.M{
	"field missing": {"$unset": bson.M{"contactPreferences.preferredChannels": ""}},
	"field null":    {"$set": bson.M{"contactPreferences.preferredChannels": nil}},
}

func makeLegacy(t *testing.T, id string, update bson.M) {
	t.Helper()
	_id, _ := primitive.ObjectIDFromHex(id)
	ctx, cancel := testDBService.getContext()
	defer cancel()
	if _, err := testDBService.collectionRefUsers(testInstanceID).UpdateOne(ctx, bson.M{"_id": _id}, update); err != nil {
		t.Fatalf("failed to write the legacy channel state: %v", err)
	}
}

// removeWhatsAppOps are the two ways a user loses the destination the whatsapp channel stood for.
var removeWhatsAppOps = map[string]func(id string, phone string) error{
	"delete": func(id string, _ string) error {
		_, err := testDBService.DeletePhoneNumber(testInstanceID, id)
		return err
	},
	"replace": func(id string, phone string) error {
		return testDBService.ReplacePhoneContactInfo(testInstanceID, id, models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone + "9",
		})
	},
}

func TestDbUpdateContactPreferencesStoresExactlyTheChosenChannels(t *testing.T) {
	for name, channels := range map[string][]string{
		"whatsapp only": {models.ChannelWhatsApp},
		"email only":    {models.ChannelEmail},
		"both":          {models.ChannelEmail, models.ChannelWhatsApp},
	} {
		t.Run(name, func(t *testing.T) {
			id := seedChannelsUser(t, fmt.Sprintf("exact_channels_%d@test.com", len(name)), "+391230000801", []string{models.ChannelEmail})
			if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{PreferredChannels: channels}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := storedChannels(t, id)
			if fmt.Sprint(got) != fmt.Sprint(channels) {
				t.Errorf("expected %v, got %v", channels, got)
			}
		})
	}
}

// Losing the phone takes the whatsapp channel away and adds nothing in its place, e-mail
// included. Only an account that had e-mail keeps it.
func TestDbLosingWhatsAppRemovesOnlyWhatsApp(t *testing.T) {
	cases := map[string]struct {
		channels []string
		want     []string
	}{
		"whatsapp only":  {[]string{models.ChannelWhatsApp}, []string{}},
		"email+whatsapp": {[]string{models.ChannelEmail, models.ChannelWhatsApp}, []string{models.ChannelEmail}},
		"whatsapp+email": {[]string{models.ChannelWhatsApp, models.ChannelEmail}, []string{models.ChannelEmail}},
		"duplicated":     {[]string{models.ChannelWhatsApp, models.ChannelWhatsApp}, []string{}},
		"email only":     {[]string{models.ChannelEmail}, []string{models.ChannelEmail}},
		"no channel":     {[]string{}, []string{}},
	}
	for opName, op := range removeWhatsAppOps {
		for caseName, c := range cases {
			t.Run(opName+"/"+caseName, func(t *testing.T) {
				phone := "+39123000090" + fmt.Sprint(len(caseName)%10)
				id := seedChannelsUser(t, fmt.Sprintf("losewa_%s_%d@test.com", opName, len(caseName)), phone, c.channels)
				if err := op(id, phone); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(c.want) == 0 {
					requireNoChannel(t, id)
					return
				}
				if got := storedChannels(t, id); fmt.Sprint(got) != fmt.Sprint(c.want) {
					t.Errorf("expected %v, got %v", c.want, got)
				}
			})
		}
		// An account written before channels existed means e-mail, and still does afterwards.
		for legacyName, legacy := range legacyChannelStates {
			t.Run(opName+"/legacy "+legacyName, func(t *testing.T) {
				phone := "+391230000913"
				id := seedChannelsUser(t, "losewa_legacy_"+opName+"@test.com", phone, nil)
				makeLegacy(t, id, legacy)
				if err := op(id, phone); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				requireOnlyEmail(t, storedChannels(t, id))
			})
		}
	}

	t.Run("delete hands back the user as stored", func(t *testing.T) {
		id := seedChannelsUser(t, "losewa_returned@test.com", "+391230000910", []string{models.ChannelWhatsApp})
		user, err := testDBService.DeletePhoneNumber(testInstanceID, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := user.ContactPreferences.PreferredChannels; got == nil || len(got) != 0 {
			t.Errorf("expected no channel in the returned user, got %#v", got)
		}
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypePhone {
				t.Errorf("the phone must be gone: %+v", ci)
			}
		}
	})

	t.Run("other contacts and settings are left as they are", func(t *testing.T) {
		id := seedChannelsUser(t, "losewa_untouched@test.com", "+391230000912", []string{models.ChannelWhatsApp})
		if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
			PreferredChannels: []string{models.ChannelWhatsApp}, SubscribedToWeekly: true,
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := removeWhatsAppOps["replace"](id, "+391230000912"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		user, _ := testDBService.GetUserByID(testInstanceID, id)
		if !user.ContactPreferences.SubscribedToWeekly {
			t.Errorf("subscription flags must survive a phone change: %+v", user.ContactPreferences)
		}
		emails := 0
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypeEmail {
				emails++
			}
		}
		if emails != 1 {
			t.Errorf("email contact info lost: %+v", user.ContactInfos)
		}
	})
}

func TestDbPreferencesCannotRestoreWhatsAppAfterRemoval(t *testing.T) {
	for opName, op := range removeWhatsAppOps {
		t.Run(opName, func(t *testing.T) {
			phone := "+391230000920"
			id := seedChannelsUser(t, "no_restore_"+opName+"@test.com", phone, []string{models.ChannelWhatsApp})
			if err := op(id, phone); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
				PreferredChannels: []string{models.ChannelWhatsApp},
			})
			if !errors.Is(err, mongo.ErrNoDocuments) {
				t.Errorf("a stale whatsapp request must be refused, got %v", err)
			}
			requireNoChannel(t, id)
		})
	}
}

// An empty list is stored as an empty array, and a nil one too, never as null or as a
// missing field (both of which read as e-mail).
func TestDbUpdateContactPreferencesStoresNoChannel(t *testing.T) {
	for name, channels := range map[string][]string{"empty": {}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			id := seedChannelsUser(t, "store_none_"+name+"@test.com", "+391230000930", []string{models.ChannelEmail, models.ChannelWhatsApp})
			got, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{PreferredChannels: channels})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ContactPreferences.PreferredChannels == nil || len(got.ContactPreferences.PreferredChannels) != 0 {
				t.Errorf("the returned user must carry the empty list, got %#v", got.ContactPreferences.PreferredChannels)
			}
			requireNoChannel(t, id)
			// And it goes out over gRPC as empty, not as the legacy e-mail default.
			if out := got.ToAPI().ContactPreferences.PreferredChannels; len(out) != 0 {
				t.Errorf("no channel must reach the API as an empty list, got %v", out)
			}
		})
	}

	t.Run("a legacy account reads as e-mail until it saves its settings", func(t *testing.T) {
		id := seedChannelsUser(t, "store_none_legacy@test.com", "+391230000931", nil)
		makeLegacy(t, id, legacyChannelStates["field missing"])
		user, err := testDBService.GetUserByID(testInstanceID, id)
		if err != nil {
			t.Fatal(err)
		}
		if out := user.ToAPI().ContactPreferences.PreferredChannels; fmt.Sprint(out) != "[email]" {
			t.Errorf("a missing field must reach the API as [email], got %v", out)
		}
		if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{PreferredChannels: []string{}}); err != nil {
			t.Fatal(err)
		}
		requireNoChannel(t, id)
	})
}

// The properties below must hold whatever the interleaving of a preferences update with the
// removal of the phone, because the services run several replicas and only the database can
// serialise them:
//   - the stored channel list is always an array: never null or missing, which would read as the
//     legacy e-mail default (it may be empty: no channel);
//   - e-mail is never added: it is stored only when a request asked for it;
//   - whatsapp is never stored while the user has no verified phone.
func assertChannelInvariants(t *testing.T, id string, context string, emailRequested bool) {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, id)
	if err != nil {
		t.Errorf("%s: failed to read user: %v", context, err)
		return
	}
	channels := user.ContactPreferences.PreferredChannels
	if channels == nil {
		t.Errorf("%s: the channel list is missing or null, which reads as e-mail", context)
	}
	if containsChannel(channels, models.ChannelEmail) && !emailRequested {
		t.Errorf("%s: e-mail was added although no request asked for it: %v", context, channels)
	}
	if containsChannel(channels, models.ChannelWhatsApp) {
		verified := false
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypePhone && ci.ConfirmedAt > 0 {
				verified = true
			}
		}
		if !verified {
			t.Errorf("%s: whatsapp stored without a verified phone: %v", context, channels)
		}
	}
}

func TestDbChannelRulesUnderConcurrentPhoneRemoval(t *testing.T) {
	const iterations = 25
	requests := [][]string{
		{models.ChannelWhatsApp},
		{models.ChannelEmail},
		{models.ChannelEmail, models.ChannelWhatsApp},
		{},
	}
	// Some writers ask for e-mail, so in these runs e-mail may legitimately be stored; the runs
	// below without such writers check that it is never added.
	emailRequested := true

	for opName, op := range removeWhatsAppOps {
		t.Run(opName+"/burst", func(t *testing.T) {
			for i := 0; i < iterations; i++ {
				phone := fmt.Sprintf("+3912300100%02d", i)
				id := seedChannelsUser(t, fmt.Sprintf("conc_burst_%s_%d@test.com", opName, i), phone, []string{models.ChannelWhatsApp})

				start := make(chan struct{})
				stopReading := make(chan struct{})
				var readers, writers sync.WaitGroup

				// A reader that watches every committed state while the writers race.
				readers.Add(1)
				go func() {
					defer readers.Done()
					for {
						select {
						case <-stopReading:
							return
						default:
							assertChannelInvariants(t, id, "observed mid-flight", emailRequested)
							// A short pause keeps the reader from saturating the shared test
							// database while still sampling many states per burst.
							time.Sleep(200 * time.Microsecond)
						}
					}
				}()

				for w := 0; w < 12; w++ {
					writers.Add(1)
					go func(w int) {
						defer writers.Done()
						<-start
						_, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
							PreferredChannels: requests[w%len(requests)],
						})
						if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
							t.Errorf("unexpected error: %v", err)
						}
					}(w)
				}
				writers.Add(1)
				go func() {
					defer writers.Done()
					<-start
					if err := op(id, phone); err != nil {
						t.Errorf("unexpected error: %v", err)
					}
				}()
				close(start)
				writers.Wait()
				close(stopReading)
				readers.Wait()

				assertChannelInvariants(t, id, "final state", emailRequested)
				if containsChannel(storedChannels(t, id), models.ChannelWhatsApp) {
					t.Fatalf("whatsapp survived the removal of its phone")
				}
			}
		})

		t.Run(opName+"/staggered", func(t *testing.T) {
			for i := 0; i < iterations; i++ {
				phone := fmt.Sprintf("+3912300200%02d", i)
				id := seedChannelsUser(t, fmt.Sprintf("conc_stagger_%s_%d@test.com", opName, i), phone, []string{models.ChannelEmail, models.ChannelWhatsApp})

				var wg sync.WaitGroup
				for w := 0; w < 6; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						time.Sleep(time.Duration(w) * 300 * time.Microsecond)
						if w == 3 {
							if err := op(id, phone); err != nil {
								t.Errorf("unexpected error: %v", err)
							}
							return
						}
						_, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
							PreferredChannels: requests[w%len(requests)],
						})
						if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
							t.Errorf("unexpected error: %v", err)
						}
					}(w)
				}
				wg.Wait()
				assertChannelInvariants(t, id, "final state", emailRequested)
			}
		})

		// Deterministic overlap: the update's guard is evaluated by the database in the same
		// write that stores the channels, so an update that starts after the phone is gone can
		// only be refused, and one that finished before it is superseded by the fallback.
		t.Run(opName+"/update before and after the removal", func(t *testing.T) {
			phone := "+391230030001"
			id := seedChannelsUser(t, "conc_overlap_"+opName+"@test.com", phone, []string{models.ChannelEmail})

			if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
				PreferredChannels: []string{models.ChannelWhatsApp},
			}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := op(id, phone); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			requireNoChannel(t, id)

			_, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
				PreferredChannels: []string{models.ChannelWhatsApp},
			})
			if !errors.Is(err, mongo.ErrNoDocuments) {
				t.Errorf("an update after the removal must be refused, got %v", err)
			}
			requireNoChannel(t, id)
		})

		// WhatsApp-only writers racing the removal: whatever the order, e-mail never appears.
		t.Run(opName+"/whatsapp-only writers never produce e-mail", func(t *testing.T) {
			for i := 0; i < iterations; i++ {
				phone := fmt.Sprintf("+3912300400%02d", i)
				id := seedChannelsUser(t, fmt.Sprintf("conc_noemail_%s_%d@test.com", opName, i), phone, []string{models.ChannelWhatsApp})
				var wg sync.WaitGroup
				start := make(chan struct{})
				for w := 0; w < 8; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						<-start
						if w == 4 {
							if err := op(id, phone); err != nil {
								t.Errorf("unexpected error: %v", err)
							}
							return
						}
						channels := []string{models.ChannelWhatsApp}
						if w%2 == 0 {
							channels = []string{}
						}
						_, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{PreferredChannels: channels})
						if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
							t.Errorf("unexpected error: %v", err)
						}
					}(w)
				}
				close(start)
				wg.Wait()
				assertChannelInvariants(t, id, "final state", false)
				requireNoChannel(t, id)
			}
		})
	}
}
