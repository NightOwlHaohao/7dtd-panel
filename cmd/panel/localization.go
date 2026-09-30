package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
)

type LocalizedText struct {
	Labels       map[string]string `json:"labels,omitempty"`
	Descriptions map[string]string `json:"descriptions,omitempty"`
}

type LocalizationCatalog struct{ byKey map[string]map[string]string }

func LoadLocalization(path string) (LocalizationCatalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return LocalizationCatalog{}, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return LocalizationCatalog{}, err
	}
	columns := make(map[string]int, len(header))
	for i, name := range header {
		columns[strings.TrimPrefix(name, "\ufeff")] = i
	}
	key, ok := columns["Key"]
	if !ok {
		return LocalizationCatalog{}, fmt.Errorf("Localization.csv has no Key column")
	}
	for _, name := range []string{"english", "schinese", "tchinese"} {
		if _, ok := columns[name]; !ok {
			return LocalizationCatalog{}, fmt.Errorf("Localization.csv has no %s column", name)
		}
	}
	catalog := LocalizationCatalog{byKey: map[string]map[string]string{}}
	for {
		record, err := r.Read()
		if err == io.EOF {
			return catalog, nil
		}
		if err != nil {
			return LocalizationCatalog{}, err
		}
		if key >= len(record) || record[key] == "" {
			continue
		}
		values := make(map[string]string, 3)
		for language, column := range map[string]string{"en": "english", "zh-CN": "schinese", "zh-TW": "tchinese"} {
			if i := columns[column]; i < len(record) && record[i] != "" {
				values[language] = record[i]
			}
		}
		catalog.byKey[record[key]] = values
	}
}

func (c LocalizationCatalog) ConfigText(property string) LocalizedText {
	text, ok := bundledConfigText[property]
	if !ok {
		text.Labels = map[string]string{"en": property}
	}
	text.Labels = copyLabels(text.Labels)
	text.Descriptions = copyLabels(text.Descriptions)
	if labels := c.byKey["go"+property]; len(labels) != 0 {
		for language, label := range labels {
			if untranslatedChinese(language, label, labels["en"]) {
				continue
			}
			text.Labels[language] = label
		}
	}
	if descriptions := c.byKey["go"+property+"Desc"]; len(descriptions) != 0 {
		if text.Descriptions == nil {
			text.Descriptions = map[string]string{}
		}
		for language, description := range descriptions {
			if untranslatedChinese(language, description, descriptions["en"]) {
				continue
			}
			text.Descriptions[language] = description
		}
	}
	return text
}

func untranslatedChinese(language, value, english string) bool {
	return (language == "zh-CN" || language == "zh-TW") && strings.TrimSpace(value) == strings.TrimSpace(english)
}

func EnrichConfig(doc ConfigDocument, catalog LocalizationCatalog) ConfigDocument {
	enriched := ConfigDocument{Hash: doc.Hash, Properties: append([]ConfigProperty(nil), doc.Properties...)}
	for i := range enriched.Properties {
		enriched.Properties[i].Text = catalog.ConfigText(enriched.Properties[i].Name)
	}
	return enriched
}

func copyLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	copy := make(map[string]string, len(labels))
	for language, value := range labels {
		copy[language] = value
	}
	return copy
}

var bundledConfigText = map[string]LocalizedText{
	"ServerName":                         labels("Server Name", "服务器名称", "伺服器名稱"),
	"ServerDescription":                  labels("Server Description", "服务器描述", "伺服器描述"),
	"ServerWebsiteURL":                   labels("Server Website URL", "服务器网站 URL", "伺服器網站 URL"),
	"ServerPassword":                     labels("Server Password", "服务器密码", "伺服器密碼"),
	"ServerPort":                         labels("Server Port", "游戏端口", "遊戲連接埠"),
	"ServerVisibility":                   labels("Server Visibility", "服务器可见性", "伺服器可見性"),
	"ServerMaxPlayerCount":               labels("Maximum Player Count", "最大玩家数量", "最大玩家數量"),
	"GameWorld":                          labels("Game World", "游戏世界", "遊戲世界"),
	"WorldGenSeed":                       labels("World Generation Seed", "世界生成种子", "世界生成種子"),
	"WorldGenSize":                       labels("World Generation Size", "世界生成大小", "世界生成大小"),
	"GameName":                           labels("Game Name", "游戏名称", "遊戲名稱"),
	"GameMode":                           labels("Game Mode", "游戏模式", "遊戲模式"),
	"UserDataFolder":                     labels("User Data Folder", "用户数据文件夹", "使用者資料夾"),
	"TelnetEnabled":                      labels("Telnet Enabled", "启用 Telnet", "啟用 Telnet"),
	"TelnetPort":                         labels("Telnet Port", "Telnet 端口", "Telnet 連接埠"),
	"WebDashboardEnabled":                labels("Web Dashboard Enabled", "启用 Web 控制面板", "啟用 Web 儀表板"),
	"WebDashboardPort":                   labels("Web Dashboard Port", "Web 控制面板端口", "Web 儀表板連接埠"),
	"SandboxCode":                        labels("Sandbox Code", "沙盒代码", "沙盒代碼"),
	"ServerLoginConfirmationText":        labels("Server Login Confirmation Text", "服务器登录确认文本", "伺服器登入確認文字"),
	"Region":                             labels("Region", "区域", "區域"),
	"Language":                           labels("Language", "语言", "語言"),
	"ServerDisabledNetworkProtocols":     labels("Disabled Network Protocols", "禁用的网络协议", "已停用的網路通訊協定"),
	"ServerMaxWorldTransferSpeedKiBs":    labels("Maximum World Transfer Speed (KiB/s)", "最大世界传输速度（KiB/秒）", "最大世界傳輸速度（KiB/秒）"),
	"ServerReservedSlots":                labels("Reserved Slots", "预留席位", "保留席位"),
	"ServerReservedSlotsPermission":      labels("Reserved Slots Permission", "预留席位权限", "保留席位權限"),
	"ServerAdminSlots":                   labels("Admin Slots", "管理员席位", "管理員席位"),
	"ServerAdminSlotsPermission":         labels("Admin Slots Permission", "管理员席位权限", "管理員席位權限"),
	"WebDashboardUrl":                    labels("Web Dashboard URL", "Web 控制面板 URL", "Web 控制台 URL"),
	"EnableMapRendering":                 labels("Enable Map Rendering", "启用地图渲染", "啟用地圖渲染"),
	"TelnetPassword":                     labels("Telnet Password", "Telnet 密码", "Telnet 密碼"),
	"TelnetFailedLoginLimit":             {Labels: labels("Telnet Failed Login Limit", "Telnet 登录失败次数上限", "Telnet 登入失敗次數上限").Labels, Descriptions: map[string]string{"zh-CN": "远程客户端输错密码达到此次数后，将被禁止连接 Telnet 接口。", "zh-TW": "遠端用戶端輸錯密碼達到此次數後，將被禁止連線至 Telnet 介面。"}},
	"TelnetFailedLoginsBlocktime":        labels("Telnet Failed Login Block Time", "Telnet 登录失败封锁时长", "Telnet 登入失敗封鎖時間"),
	"TerminalWindowEnabled":              labels("Terminal Window Enabled", "启用终端窗口", "啟用終端機視窗"),
	"AdminFileName":                      labels("Admin File Name", "管理员文件名", "管理員檔案名稱"),
	"ServerAllowCrossplay":               labels("Allow Crossplay", "允许跨平台联机", "允許跨平台連線"),
	"EACEnabled":                         labels("Easy Anti-Cheat Enabled", "启用 Easy Anti-Cheat", "啟用 Easy Anti-Cheat"),
	"IgnoreEOSSanctions":                 labels("Ignore EOS Sanctions", "忽略 EOS 处罚", "忽略 EOS 處罰"),
	"HideCommandExecutionLog":            labels("Hide Command Execution Log", "隐藏命令执行日志", "隱藏命令執行日誌"),
	"MaxUncoveredMapChunksPerPlayer":     labels("Maximum Uncovered Map Chunks per Player", "每名玩家最大未覆盖地图区块数", "每名玩家最大未覆蓋地圖區塊數"),
	"PersistentPlayerProfiles":           labels("Persistent Player Profiles", "持久化玩家档案", "持久化玩家設定檔"),
	"MaxChunkAge":                        labels("Maximum Chunk Age", "区块最大保留时长", "區塊最大保留時間"),
	"SaveDataLimit":                      labels("Save Data Limit", "存档数据上限", "存檔資料上限"),
	"PlayerSafeZoneLevel":                labels("Player Safe Zone Level", "玩家安全区等级", "玩家安全區等級"),
	"PlayerSafeZoneHours":                labels("Player Safe Zone Hours", "玩家安全区时长", "玩家安全區時數"),
	"BuildCreate":                        labels("Creative Mode", "创意模式", "創意模式"),
	"BedrollDeadZoneSize":                labels("Bedroll Dead Zone Size", "睡袋禁建区大小", "睡袋禁建區大小"),
	"BedrollExpiryTime":                  labels("Bedroll Expiry Time", "睡袋过期时间", "睡袋到期時間"),
	"AllowSpawnNearFriend":               labels("Allow Spawn Near Friend", "允许在好友附近生成", "允許在好友附近生成"),
	"CameraRestrictionMode":              labels("Camera Restriction Mode", "镜头限制模式", "鏡頭限制模式"),
	"MaxSpawnedZombies":                  labels("Maximum Spawned Zombies", "最大生成僵尸数", "最大生成殭屍數"),
	"MaxSpawnedAnimals":                  labels("Maximum Spawned Animals", "最大生成动物数", "最大生成動物數"),
	"ServerMaxAllowedViewDistance":       labels("Maximum Allowed View Distance", "服务器允许的最大视距", "伺服器允許的最大視距"),
	"MaxQueuedMeshLayers":                labels("Maximum Queued Mesh Layers", "最大排队网格层数", "最大佇列網格層數"),
	"PartySharedKillRange":               labels("Party Shared Kill Range", "队伍共享击杀范围", "隊伍共享擊殺範圍"),
	"PlayerKillingMode":                  labels("Player Killing Mode", "玩家击杀模式", "玩家擊殺模式"),
	"LandClaimCount":                     labels("Land Claim Count", "领地声明数量", "領地宣告數量"),
	"LandClaimSize":                      labels("Land Claim Size", "领地声明大小", "領地宣告大小"),
	"LandClaimDeadZone":                  labels("Land Claim Dead Zone", "领地声明禁建区", "領地宣告禁建區"),
	"LandClaimExpiryTime":                labels("Land Claim Expiry Time", "领地声明过期时间", "領地宣告到期時間"),
	"LandClaimDecayMode":                 labels("Land Claim Decay Mode", "领地声明衰减模式", "領地宣告衰減模式"),
	"LandClaimOnlineDurabilityModifier":  labels("Land Claim Online Durability Modifier", "领地声明在线耐久度修正", "領地宣告在線耐久度修正"),
	"LandClaimOfflineDurabilityModifier": labels("Land Claim Offline Durability Modifier", "领地声明离线耐久度修正", "領地宣告離線耐久度修正"),
	"LandClaimOfflineDelay":              labels("Land Claim Offline Delay", "领地声明离线延迟", "領地宣告離線延遲"),
	"DynamicMeshEnabled":                 labels("Dynamic Mesh Enabled", "启用动态网格", "啟用動態網格"),
	"DynamicMeshLandClaimOnly":           labels("Dynamic Mesh Land Claim Only", "动态网格仅用于领地声明", "動態網格僅用於領地宣告"),
	"DynamicMeshLandClaimBuffer":         labels("Dynamic Mesh Land Claim Buffer", "动态网格领地声明缓冲区", "動態網格領地宣告緩衝區"),
	"DynamicMeshMaxItemCache":            labels("Dynamic Mesh Maximum Item Cache", "动态网格最大物品缓存", "動態網格最大物品快取"),
	"TwitchServerPermission":             labels("Twitch Server Permission", "Twitch 服务器权限", "Twitch 伺服器權限"),
	"TwitchBloodMoonAllowed":             labels("Twitch Blood Moon Allowed", "允许 Twitch 血月", "允許 Twitch 血月"),
}

func labels(en, zhCN, zhTW string) LocalizedText {
	return LocalizedText{Labels: map[string]string{"en": en, "zh-CN": zhCN, "zh-TW": zhTW}}
}
