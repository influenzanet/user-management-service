package models

import (
	"errors"
	"time"

	"github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// User describes the user as saved in the DB
type User struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"user_id,omitempty"`
	Account            Account            `bson:"account"`
	Roles              []string           `bson:"roles" json:"roles"`
	Timestamps         Timestamps         `bson:"timestamps"`
	Profiles           []Profile          `bson:"profiles"`
	ContactPreferences ContactPreferences `bson:"contactPreferences"`
	ContactInfos       []ContactInfo      `bson:"contactInfos"`
}

// ToAPI converts the object from DB to API format
func (u User) ToAPI() *api.User {
	profiles := make([]*api.Profile, len(u.Profiles))
	for i, p := range u.Profiles {
		profiles[i] = p.ToAPI()
	}
	contactInfos := make([]*api.ContactInfo, len(u.ContactInfos))
	for i, c := range u.ContactInfos {
		contactInfos[i] = c.ToAPI()
	}
	return &api.User{
		Id:                 u.ID.Hex(),
		Account:            u.Account.ToAPI(),
		Roles:              u.Roles,
		Timestamps:         u.Timestamps.ToAPI(),
		Profiles:           profiles,
		ContactPreferences: u.ContactPreferences.ToAPI(),
		ContactInfos:       contactInfos,
	}
}

// HasRole checks whether the user has a specified role
func (u User) HasRole(role string) bool {
	for _, v := range u.Roles {
		if v == role {
			return true
		}
	}
	return false
}

// AddRole to append a role to the user
func (u *User) AddRole(role string) error {
	for _, v := range u.Roles {
		if v == role {
			return errors.New("role already added")
		}
	}
	u.Roles = append(u.Roles, role)
	return nil
}

// RemoveRole to append a role to the user
func (u *User) RemoveRole(role string) error {
	for i, ci := range u.Roles {
		if ci == role {
			u.Roles = append(u.Roles[:i], u.Roles[i+1:]...)
			return nil
		}
	}
	return errors.New("role not found")
}

// Add a new email address
func (u *User) AddNewEmail(addr string, confirmed bool) {
	contactInfo := ContactInfo{
		ID:          primitive.NewObjectID(),
		Type:        "email",
		ConfirmedAt: 0,
		Email:       addr,
	}
	if confirmed {
		contactInfo.ConfirmedAt = time.Now().Unix()
	}
	u.ContactInfos = append(u.ContactInfos, contactInfo)
}

func (u *User) ConfirmContactInfo(t string, addr string) error {
	for i, ci := range u.ContactInfos {
		if t == "email" && ci.Email == addr {
			u.ContactInfos[i].ConfirmedAt = time.Now().Unix()
			return nil
		} else if t == "phone" && ci.Phone == addr {
			u.ContactInfos[i].ConfirmedAt = time.Now().Unix()
			return nil
		}
	}
	return errors.New("contact not found")
}

func (u *User) SetContactInfoVerificationSent(t string, addr string) {
	for i, ci := range u.ContactInfos {
		if t == "email" && ci.Email == addr {
			u.ContactInfos[i].ConfirmationLinkSentAt = time.Now().Unix()
			return
		} else if t == "phone" && ci.Phone == addr {
			u.ContactInfos[i].ConfirmationLinkSentAt = time.Now().Unix()
			return
		}
	}
}

func (u User) FindContactInfoByTypeAndAddr(t string, addr string) (ContactInfo, bool) {
	for _, ci := range u.ContactInfos {
		if t == "email" && ci.Email == addr {
			return ci, true
		} else if t == "phone" && ci.Phone == addr {
			return ci, true
		}
	}
	return ContactInfo{}, false
}

func (u User) FindContactInfoById(id string) (ContactInfo, bool) {
	for _, ci := range u.ContactInfos {
		if ci.ID.Hex() == id {
			return ci, true
		}
	}
	return ContactInfo{}, false
}

// RemoveContactInfo from the user and also all references from the contact preferences
func (u *User) RemoveContactInfo(id string) error {
	for i, ci := range u.ContactInfos {
		if ci.ID.Hex() == id {
			if u.Account.Type == "email" && ci.Email == u.Account.AccountID {
				return errors.New("cannot remove main address")
			}

			u.ContactInfos = append(u.ContactInfos[:i], u.ContactInfos[i+1:]...)
			return nil
		}
	}
	u.RemoveContactInfoFromContactPreferences(id)
	return errors.New("contact not found")
}

// RemoveContactInfoFromContactPreferences should delete all references to a contact info object
func (u *User) RemoveContactInfoFromContactPreferences(id string) {
	// remove address from contact preferences
	for i, addrRef := range u.ContactPreferences.SendNewsletterTo {
		if addrRef == id {
			u.ContactPreferences.SendNewsletterTo = append(u.ContactPreferences.SendNewsletterTo[:i], u.ContactPreferences.SendNewsletterTo[i+1:]...)
			return
		}
	}
}

// ReplaceContactInfoInContactPreferences to use if a new contact reference should replace to old one
func (u *User) ReplaceContactInfoInContactPreferences(oldId string, newId string) {
	// replace address from contact preferences
	for i, addrRef := range u.ContactPreferences.SendNewsletterTo {
		if addrRef == oldId {
			u.ContactPreferences.SendNewsletterTo[i] = newId
		}
	}
}

// AddProfile generates unique ID and adds profile to the user's array
func (u *User) AddProfile(p Profile) {
	p.ID = primitive.NewObjectID()
	p.CreatedAt = time.Now().Unix()
	u.Profiles = append(u.Profiles, p)
}

// UpdateProfile finds and replaces profile in the user's array
func (u *User) UpdateProfile(p Profile) error {
	for i, cP := range u.Profiles {
		if cP.ID == p.ID {
			p.MainProfile = cP.MainProfile
			u.Profiles[i] = p
			return nil
		}
	}
	return errors.New("profile with given ID not found")
}

// FindProfile finds a profile in the user's array
func (u User) FindProfile(id string) (Profile, error) {
	for _, cP := range u.Profiles {
		if cP.ID.Hex() == id {
			return cP, nil
		}
	}
	return Profile{}, errors.New("profile with given ID not found")
}

// RemoveProfile finds and removes profile from the user's array
func (u *User) RemoveProfile(id string) error {
	for i, cP := range u.Profiles {
		if cP.ID.Hex() == id {
			if cP.MainProfile {
				return errors.New("cannot remove main profile")
			}
			u.Profiles = append(u.Profiles[:i], u.Profiles[i+1:]...)
			return nil
		}
	}
	return errors.New("profile with given ID not found")
}

// Timestamps describes metadata for the User
// createdAt contains the account creation time, an offset is added if this account is created by admin, to reduce
// risk this account to be deleled if account verification is not done in time (use case of migration when users are invited from previous platfom).
// The offset applied is defined in pkg/grpc/service/constant.go
type Timestamps struct {
	LastTokenRefresh        int64 `bson:"lastTokenRefresh"`
	LastLogin               int64 `bson:"lastLogin"`
	CreatedAt               int64 `bson:"createdAt"`
	UpdatedAt               int64 `bson:"updatedAt"`
	LastPasswordChange      int64 `bson:"lastPasswordChange"`
	ReminderToConfirmSentAt int64 `bson:"reminderToConfirmSentAt"`
}

// ToAPI converts the object from DB to API format
func (o Timestamps) ToAPI() *api.User_Timestamps {
	return &api.User_Timestamps{
		LastTokenRefresh:   o.LastTokenRefresh,
		LastLogin:          o.LastLogin,
		CreatedAt:          o.CreatedAt,
		UpdatedAt:          o.UpdatedAt,
		LastPasswordChange: o.LastPasswordChange,
	}
}
