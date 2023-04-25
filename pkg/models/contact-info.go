package models

import (
	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ContactInfo is about saving infos about communication channels
type ContactInfo struct {
	ID                     primitive.ObjectID `bson:"_id,omitempty"`
	Type                   string             `bson:"type,omitempty"`
	ConfirmedAt            int64              `bson:"confirmedAt"`
	ConfirmationLinkSentAt int64              `bson:"confirmationLinkSentAt"`
	Email                  string             `bson:"email,omitempty"`
	Phone                  string             `bson:"phone,omitempty"`
}

func ContactInfoFromAPI(obj *api.ContactInfo) ContactInfo {
	if obj == nil {
		return ContactInfo{}
	}
	_id, _ := primitive.ObjectIDFromHex(obj.Id)
	res := ContactInfo{
		ID:          _id,
		Type:        obj.Type,
		ConfirmedAt: obj.ConfirmedAt,
	}

	switch x := obj.Address.(type) {
	case *api.ContactInfo_Email:
		res.Email = x.Email
	case *api.ContactInfo_Phone:
		res.Phone = x.Phone
	case nil:
		// The field is not set.
	default:
		logger.Warning.Printf("api.ContactInfo has unexpected type %T", x)
	}
	return res
}

// ToAPI converts a person from DB format into the API format
func (obj ContactInfo) ToAPI() *api.ContactInfo {
	res := &api.ContactInfo{
		Id:          obj.ID.Hex(),
		Type:        obj.Type,
		ConfirmedAt: obj.ConfirmedAt,
	}
	if len(obj.Email) > 0 {
		res.Address = &api.ContactInfo_Email{Email: obj.Email}
	} else if len(obj.Phone) > 0 {
		res.Address = &api.ContactInfo_Phone{Phone: obj.Phone}
	}
	return res
}
