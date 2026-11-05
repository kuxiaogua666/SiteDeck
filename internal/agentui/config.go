package agentui

import "sitedeck/internal/agentconfig"

type Settings = agentconfig.Settings
type SettingsView = agentconfig.SettingsView

var DefaultDirectory = agentconfig.DefaultDirectory
var DefaultSettings = agentconfig.DefaultSettings
var LoadSettings = agentconfig.LoadSettings
var ValidateSettings = agentconfig.ValidateSettings
var writePrivate = agentconfig.WritePrivate
