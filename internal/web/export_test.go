package web

// SetChannelAPIs points Telegram, PagerDuty and Twilio at a test server.
func SetChannelAPIs(base string) {
	telegramAPI, pagerDutyAPI, twilioAPI = base+"/telegram", base+"/pagerduty", base+"/twilio"
}
