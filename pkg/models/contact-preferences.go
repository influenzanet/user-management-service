package models

import "github.com/influenzanet/user-management-service/pkg/api"

// ContactPreferences defines how to reach out to the user for what purpose
type ContactPreferences struct {
	SubscribedToNewsletter        bool     `bson:"subscribedToNewsletter"`
	SendNewsletterTo              []string `bson:"sendNewsletterTo"`
	SubscribedToWeekly            bool     `bson:"subscribedToWeekly"`
	ReceiveWeeklyMessageDayOfWeek int32    `bson:"receiveWeeklyMessageDayOfWeek"`
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
	}
}
