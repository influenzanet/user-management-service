package models

import (
	"github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Profile describes personal profile information for a User
type Profile struct {
	ID                   primitive.ObjectID `bson:"_id,omitempty"`
	Alias                string             `bson:"alias,omitempty"`
	ConsentConfirmedAt   int64              `bson:"consentConfirmedAt"`
	CreatedAt            int64              `bson:"createdAt"`
	AvatarID             string             `bson:"avatarID,omitempty"`
	MainProfile          bool               `bson:"mainProfile"`
	AcceptedPolicyChange string             `bson:"acceptedPolicyChange"`
}

func ProfileFromAPI(p *api.Profile) Profile {
	if p == nil {
		return Profile{}
	}
	dbProf := Profile{
		Alias:                p.Alias,
		ConsentConfirmedAt:   p.ConsentConfirmedAt,
		CreatedAt:            p.CreatedAt,
		AvatarID:             p.AvatarId,
		MainProfile:          p.MainProfile,
		AcceptedPolicyChange: p.AcceptedPolicyChange,
	}
	if len(p.Id) > 0 {
		_id, _ := primitive.ObjectIDFromHex(p.Id)
		dbProf.ID = _id
	}
	return dbProf
}

// ToAPI converts a person from DB format into the API format
func (p Profile) ToAPI() *api.Profile {
	return &api.Profile{
		Id:                   p.ID.Hex(),
		Alias:                p.Alias,
		ConsentConfirmedAt:   p.ConsentConfirmedAt,
		CreatedAt:            p.CreatedAt,
		AvatarId:             p.AvatarID,
		MainProfile:          p.MainProfile,
		AcceptedPolicyChange: p.AcceptedPolicyChange,
	}
}
