package models

import (
	"testing"

	"github.com/influenzanet/user-management-service/pkg/api"
)

func TestContactPreferencesFromAPI_NoWhatsappNumber(t *testing.T) {
	// Proto ContactPreferences no longer has WhatsappNumber field (reserved 6).
	// FromAPI must map only the active fields without error.
	input := &api.ContactPreferences{
		SubscribedToNewsletter:        true,
		SendNewsletterTo:              []string{"ci1", "ci2"},
		SubscribedToWeekly:            true,
		ReceiveWeeklyMessageDayOfWeek: 3,
		PreferredChannels:             []string{"email", "whatsapp"},
	}

	result := ContactPreferencesFromAPI(input)

	if !result.SubscribedToNewsletter {
		t.Error("SubscribedToNewsletter not mapped")
	}
	if len(result.SendNewsletterTo) != 2 {
		t.Errorf("SendNewsletterTo: got %d items, want 2", len(result.SendNewsletterTo))
	}
	if !result.SubscribedToWeekly {
		t.Error("SubscribedToWeekly not mapped")
	}
	if result.ReceiveWeeklyMessageDayOfWeek != 3 {
		t.Errorf("ReceiveWeeklyMessageDayOfWeek: got %d, want 3", result.ReceiveWeeklyMessageDayOfWeek)
	}
}

func TestContactPreferencesFromAPI_Nil(t *testing.T) {
	result := ContactPreferencesFromAPI(nil)

	if result.SubscribedToNewsletter || result.SubscribedToWeekly {
		t.Error("expected zero-value ContactPreferences for nil input")
	}
}

func TestContactPreferencesToAPI_NoWhatsappNumber(t *testing.T) {
	// ToAPI must produce a proto without WhatsappNumber (field no longer exists).
	prefs := ContactPreferences{
		SubscribedToNewsletter:        true,
		SendNewsletterTo:              []string{"ci1"},
		SubscribedToWeekly:            false,
		ReceiveWeeklyMessageDayOfWeek: 5,
	}

	result := prefs.ToAPI()

	if !result.SubscribedToNewsletter {
		t.Error("SubscribedToNewsletter not mapped")
	}
	if len(result.SendNewsletterTo) != 1 {
		t.Errorf("SendNewsletterTo: got %d items, want 1", len(result.SendNewsletterTo))
	}
	if result.SubscribedToWeekly {
		t.Error("SubscribedToWeekly should be false")
	}
	if result.ReceiveWeeklyMessageDayOfWeek != 5 {
		t.Errorf("ReceiveWeeklyMessageDayOfWeek: got %d, want 5", result.ReceiveWeeklyMessageDayOfWeek)
	}
}

func TestContactPreferencesFromAPI_WithPreferredChannels(t *testing.T) {
	input := &api.ContactPreferences{
		SubscribedToNewsletter: true,
		SubscribedToWeekly:     true,
		PreferredChannels:      []string{"email", "whatsapp"},
	}
	result := ContactPreferencesFromAPI(input)

	if len(result.PreferredChannels) != 2 {
		t.Errorf("PreferredChannels: got %d items, want 2", len(result.PreferredChannels))
	}
	if result.PreferredChannels[0] != "email" || result.PreferredChannels[1] != "whatsapp" {
		t.Errorf("PreferredChannels: got %v, want [email whatsapp]", result.PreferredChannels)
	}
}

func TestContactPreferencesToAPI_WithPreferredChannels(t *testing.T) {
	prefs := ContactPreferences{
		SubscribedToNewsletter: true,
		PreferredChannels:      []string{"whatsapp"},
	}
	result := prefs.ToAPI()

	if len(result.PreferredChannels) != 1 || result.PreferredChannels[0] != "whatsapp" {
		t.Errorf("PreferredChannels: got %v, want [whatsapp]", result.PreferredChannels)
	}
}

func TestContactPreferencesFromAPI_EmptyChannels(t *testing.T) {
	input := &api.ContactPreferences{
		SubscribedToNewsletter: true,
	}
	result := ContactPreferencesFromAPI(input)

	if result.PreferredChannels != nil {
		t.Errorf("PreferredChannels: got %v, want nil", result.PreferredChannels)
	}
}

func TestContactPreferencesZeroValue(t *testing.T) {
	prefs := ContactPreferences{}
	result := prefs.ToAPI()

	if result.SubscribedToNewsletter || result.SubscribedToWeekly {
		t.Error("expected zero-value proto for zero-value struct")
	}
	if result.ReceiveWeeklyMessageDayOfWeek != 0 {
		t.Error("expected 0 for ReceiveWeeklyMessageDayOfWeek on zero-value")
	}
}
