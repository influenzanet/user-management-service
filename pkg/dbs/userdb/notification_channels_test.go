package userdb

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
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

func TestDbLosingWhatsAppFallsBackToEmail(t *testing.T) {
	cases := map[string][]string{
		"whatsapp only":  {models.ChannelWhatsApp},
		"email+whatsapp": {models.ChannelEmail, models.ChannelWhatsApp},
		"whatsapp+email": {models.ChannelWhatsApp, models.ChannelEmail},
		"duplicated":     {models.ChannelWhatsApp, models.ChannelWhatsApp},
	}
	for opName, op := range removeWhatsAppOps {
		for caseName, channels := range cases {
			t.Run(opName+"/"+caseName, func(t *testing.T) {
				phone := "+39123000090" + fmt.Sprint(len(caseName)%10)
				id := seedChannelsUser(t, fmt.Sprintf("fallback_%s_%d@test.com", opName, len(caseName)), phone, channels)
				if err := op(id, phone); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				requireOnlyEmail(t, storedChannels(t, id))
			})
		}
	}

	t.Run("delete returns the fallback in the user it hands back", func(t *testing.T) {
		id := seedChannelsUser(t, "fallback_returned@test.com", "+391230000910", []string{models.ChannelWhatsApp})
		user, err := testDBService.DeletePhoneNumber(testInstanceID, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		requireOnlyEmail(t, user.ContactPreferences.PreferredChannels)
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypePhone {
				t.Errorf("the phone must be gone: %+v", ci)
			}
		}
	})

	t.Run("an email-only user stays email-only", func(t *testing.T) {
		id := seedChannelsUser(t, "fallback_email_only@test.com", "+391230000911", []string{models.ChannelEmail})
		if _, err := testDBService.DeletePhoneNumber(testInstanceID, id); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		requireOnlyEmail(t, storedChannels(t, id))
	})

	t.Run("other contacts and settings are left as they are", func(t *testing.T) {
		id := seedChannelsUser(t, "fallback_untouched@test.com", "+391230000912", []string{models.ChannelWhatsApp})
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

func TestDbPreferencesCannotRestoreWhatsAppAfterFallback(t *testing.T) {
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
			requireOnlyEmail(t, storedChannels(t, id))
		})
	}
}

// The properties below must hold whatever the interleaving of a preferences update with the
// removal of the phone, because the services run several replicas and only the database can
// serialise them:
//   - the stored channel list is never empty or missing;
//   - whatsapp is never stored while the user has no verified phone.
func assertChannelInvariants(t *testing.T, id string, context string) {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, id)
	if err != nil {
		t.Errorf("%s: failed to read user: %v", context, err)
		return
	}
	channels := user.ContactPreferences.PreferredChannels
	if len(channels) == 0 {
		t.Errorf("%s: the user was left without any channel", context)
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
	}

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
							assertChannelInvariants(t, id, "observed mid-flight")
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

				assertChannelInvariants(t, id, "final state")
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
				assertChannelInvariants(t, id, "final state")
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
			requireOnlyEmail(t, storedChannels(t, id))

			_, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
				PreferredChannels: []string{models.ChannelWhatsApp},
			})
			if !errors.Is(err, mongo.ErrNoDocuments) {
				t.Errorf("an update after the removal must be refused, got %v", err)
			}
			requireOnlyEmail(t, storedChannels(t, id))
		})
	}
}
