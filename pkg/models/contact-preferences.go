package models

import "github.com/influenzanet/user-management-service/pkg/api"

// ContactPreferences defines how to reach out to the user for what purpose
type ContactPreferences struct {
	SubscribedToNewsletter        bool     `bson:"subscribedToNewsletter"`
	SendNewsletterTo              []string `bson:"sendNewsletterTo"`
	SubscribedToWeekly            bool     `bson:"subscribedToWeekly"`
	ReceiveWeeklyMessageDayOfWeek int32    `bson:"receiveWeeklyMessageDayOfWeek"`
	// PreferredChannels lists the channels notifications go out on. An empty list means none:
	// the participant gets no notification on any channel. A missing field (nil) is an account
	// written before channels existed, and means e-mail (see EffectivePreferredChannels).
	PreferredChannels []string `bson:"preferredChannels"`
}

// DefaultPreferredChannels is what a new account starts with, and what an account written
// before channels existed is read as.
func DefaultPreferredChannels() []string {
	return []string{ChannelEmail}
}

// EffectivePreferredChannels resolves the stored list: nil (field missing, legacy account) reads
// as e-mail, an empty list stays empty. Proto3 cannot tell an empty repeated field from a missing
// one, so the difference has to be settled here, before the list leaves the service.
func (obj ContactPreferences) EffectivePreferredChannels() []string {
	if obj.PreferredChannels == nil {
		return DefaultPreferredChannels()
	}
	return obj.PreferredChannels
}

// HasPreferredChannel reports whether notifications go out on the channel, legacy default
// included.
func (obj ContactPreferences) HasPreferredChannel(channel string) bool {
	for _, c := range obj.EffectivePreferredChannels() {
		if c == channel {
			return true
		}
	}
	return false
}

func ContactPreferencesFromAPI(obj *api.ContactPreferences) ContactPreferences {
	if obj == nil {
		return ContactPreferences{}
	}

	res := ContactPreferences{
		SubscribedToNewsletter:        obj.SubscribedToNewsletter,
		SendNewsletterTo:              obj.SendNewsletterTo,
		SubscribedToWeekly:            obj.SubscribedToWeekly,
		ReceiveWeeklyMessageDayOfWeek: obj.ReceiveWeeklyMessageDayOfWeek,
		PreferredChannels:             obj.PreferredChannels,
	}
	return res
}

// ToAPI converts a person from DB format into the API format
func (obj ContactPreferences) ToAPI() *api.ContactPreferences {
	return &api.ContactPreferences{
		SubscribedToNewsletter:        obj.SubscribedToNewsletter,
		SendNewsletterTo:              obj.SendNewsletterTo,
		SubscribedToWeekly:            obj.SubscribedToWeekly,
		ReceiveWeeklyMessageDayOfWeek: obj.ReceiveWeeklyMessageDayOfWeek,
		PreferredChannels:             obj.EffectivePreferredChannels(),
	}
}
