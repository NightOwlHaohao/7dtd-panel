package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalizationCatalogReadsOfficialChinese(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Localization.csv")
	data := "\ufeffKey,File,Type,UsedInMainMenu,NoTranslate,KeepLoaded,english,Context / Alternate Text,schinese,tchinese\n" +
		"goBloodMoonFrequency,UI,Menu,x,,,Blood Moon Frequency,,血月频率,血月頻率\n" +
		"goBloodMoonFrequencyDesc,UI,Menu,x,,,\"Days between blood moons, inclusive.\",,两次血月间隔天数。,兩次血月間隔天數。\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadLocalization(path)
	if err != nil {
		t.Fatal(err)
	}
	got := catalog.ConfigText("BloodMoonFrequency")
	if got.Labels["zh-CN"] != "血月频率" || got.Labels["zh-TW"] != "血月頻率" || got.Labels["en"] != "Blood Moon Frequency" {
		t.Fatalf("labels=%#v", got.Labels)
	}
	if got.Descriptions["en"] != "Days between blood moons, inclusive." || got.Descriptions["zh-CN"] != "两次血月间隔天数。" {
		t.Fatalf("descriptions=%#v", got.Descriptions)
	}
}

func TestConfigMetadataFallsBackToBundledServerText(t *testing.T) {
	got := (LocalizationCatalog{}).ConfigText("ServerPort")
	if got.Labels["zh-CN"] != "游戏端口" || got.Labels["en"] != "Server Port" {
		t.Fatalf("got=%#v", got)
	}
}

func TestBundledTextCoversDedicatedServerPropertiesMissingFromOfficialLocalization(t *testing.T) {
	properties := []string{
		"ServerLoginConfirmationText", "Region", "Language", "ServerDisabledNetworkProtocols", "ServerMaxWorldTransferSpeedKiBs", "ServerReservedSlots", "ServerReservedSlotsPermission", "ServerAdminSlots", "ServerAdminSlotsPermission", "WebDashboardUrl", "EnableMapRendering", "TelnetPassword", "TelnetFailedLoginLimit", "TelnetFailedLoginsBlocktime", "TerminalWindowEnabled", "AdminFileName", "ServerAllowCrossplay", "EACEnabled", "IgnoreEOSSanctions", "HideCommandExecutionLog", "MaxUncoveredMapChunksPerPlayer", "PersistentPlayerProfiles", "MaxChunkAge", "SaveDataLimit", "PlayerSafeZoneLevel", "PlayerSafeZoneHours", "BuildCreate", "BedrollDeadZoneSize", "BedrollExpiryTime", "AllowSpawnNearFriend", "CameraRestrictionMode", "MaxSpawnedZombies", "MaxSpawnedAnimals", "ServerMaxAllowedViewDistance", "MaxQueuedMeshLayers", "PartySharedKillRange", "PlayerKillingMode", "LandClaimCount", "LandClaimSize", "LandClaimDeadZone", "LandClaimExpiryTime", "LandClaimDecayMode", "LandClaimOnlineDurabilityModifier", "LandClaimOfflineDurabilityModifier", "LandClaimOfflineDelay", "DynamicMeshEnabled", "DynamicMeshLandClaimOnly", "DynamicMeshLandClaimBuffer", "DynamicMeshMaxItemCache", "TwitchServerPermission", "TwitchBloodMoonAllowed",
	}
	for _, property := range properties {
		got := (LocalizationCatalog{}).ConfigText(property)
		for _, language := range []string{"en", "zh-CN", "zh-TW"} {
			if got.Labels[language] == "" || (language != "en" && got.Labels[language] == property) {
				t.Errorf("%s has no %s bundled label: %#v", property, language, got.Labels)
			}
		}
	}
}

func TestBundledDescriptionsAreChineseOnlyWhenVerified(t *testing.T) {
	got := (LocalizationCatalog{}).ConfigText("TelnetFailedLoginLimit")
	if got.Descriptions["zh-CN"] == "" || got.Descriptions["zh-TW"] == "" {
		t.Fatalf("TelnetFailedLoginLimit needs verified Chinese descriptions: %#v", got.Descriptions)
	}
	if got := (LocalizationCatalog{}).ConfigText("TwitchBloodMoonAllowed"); len(got.Descriptions) != 0 {
		t.Fatalf("unknown description must stay empty, got %#v", got.Descriptions)
	}
}

func TestOfficialEnglishCopiesDoNotReplaceBundledChinese(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Localization.csv")
	data := "\ufeffKey,File,Type,UsedInMainMenu,NoTranslate,KeepLoaded,english,Context / Alternate Text,schinese,tchinese\n" +
		"goBuildCreate,UI,Menu,x,,,Creative Mode,,Creative Mode,Creative Mode\n" +
		"goBuildCreateDesc,UI,Menu,x,,,Toggle creative mode.,,Toggle creative mode.,Toggle creative mode.\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadLocalization(path)
	if err != nil {
		t.Fatal(err)
	}
	got := catalog.ConfigText("BuildCreate")
	if got.Labels["en"] != "Creative Mode" || got.Labels["zh-CN"] != "创意模式" || got.Labels["zh-TW"] != "創意模式" {
		t.Fatalf("labels=%#v", got.Labels)
	}
	if got.Descriptions["en"] != "Toggle creative mode." || got.Descriptions["zh-CN"] != "" || got.Descriptions["zh-TW"] != "" {
		t.Fatalf("descriptions=%#v", got.Descriptions)
	}
}
