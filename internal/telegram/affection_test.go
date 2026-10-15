package telegram

import "testing"

func TestAffectionKeyboardUsesKissInsteadOfAttention(t *testing.T) {
	keyboard := affectionKeyboard()
	callbacks := map[string]string{}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			callbacks[button.CallbackData] = button.Text
		}
	}
	if callbacks["affection:kiss"] != "Хочу поцеловать" {
		t.Fatal("kiss button is missing", callbacks)
	}
	if _, exists := callbacks["affection:attention"]; exists {
		t.Fatal("removed attention button is still visible", callbacks)
	}
}
