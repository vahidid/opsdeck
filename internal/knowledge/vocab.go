package knowledge

// vocab maps Persian operations words to the English words skills and
// runbooks are written with, so a request in Persian finds them too.
var vocab = map[string][]string{
	"سرور":       {"server"},
	"سرورها":     {"server"},
	"هاست":       {"host"},
	"دیسک":       {"disk", "storage"},
	"حافظه":      {"memory", "ram"},
	"رم":         {"memory", "ram"},
	"پردازنده":   {"cpu"},
	"شبکه":       {"network"},
	"فایروال":    {"firewall"},
	"دیوارآتش":   {"firewall"},
	"گواهی":      {"certificate", "tls"},
	"گواهینامه":  {"certificate", "tls"},
	"پشتیبان":    {"backup"},
	"بکاپ":       {"backup"},
	"بازیابی":    {"restore", "recovery"},
	"استقرار":    {"deploy", "deployment"},
	"دیپلوی":     {"deploy", "deployment"},
	"انتشار":     {"release", "deploy"},
	"مانیتورینگ": {"monitoring", "prometheus", "alert"},
	"پایش":       {"monitoring"},
	"هشدار":      {"alert", "alerting"},
	"لاگ":        {"log", "logging"},
	"لاگها":      {"log", "logging"},
	"امنیت":      {"security", "hardening"},
	"امن":        {"security", "hardening"},
	"سختسازی":    {"hardening"},
	"کانتینر":    {"container", "docker"},
	"داکر":       {"docker"},
	"کوبرنتیز":   {"kubernetes", "k8s"},
	"کوبر":       {"kubernetes"},
	"کلاستر":     {"cluster", "kubernetes"},
	"خوشه":       {"cluster"},
	"دیتابیس":    {"database"},
	"پایگاه":     {"database"},
	"داده":       {"data", "database"},
	"کاربر":      {"user"},
	"دسترسی":     {"access", "permission"},
	"رمز":        {"password", "secret"},
	"کلید":       {"key", "ssh"},
	"اتصال":      {"connection", "connectivity"},
	"قطع":        {"outage", "down", "connectivity"},
	"خطا":        {"error", "failure"},
	"مشکل":       {"issue", "troubleshoot"},
	"عیبیابی":    {"troubleshoot", "debug"},
	"دیباگ":      {"debug", "troubleshoot"},
	"حادثه":      {"incident"},
	"رخداد":      {"incident"},
	"پستمورتم":   {"postmortem"},
	"کندی":       {"slow", "latency", "performance"},
	"کند":        {"slow", "latency", "performance"},
	"کارایی":     {"performance"},
	"مقیاس":      {"scale", "scaling"},
	"نسخه":       {"version", "release"},
	"بروزرسانی":  {"update", "upgrade"},
	"آپدیت":      {"update", "upgrade"},
	"ارتقا":      {"upgrade"},
	"نصب":        {"install", "setup"},
	"راهاندازی":  {"setup", "bootstrap", "install"},
	"پیکربندی":   {"config", "configuration"},
	"کانفیگ":     {"config", "configuration"},
	"تنظیمات":    {"config", "settings"},
	"اسکریپت":    {"script"},
	"مستندات":    {"docs", "documentation", "runbook"},
	"ران‌بوک":    {"runbook"},
	"رانبوک":     {"runbook"},
	"گزارش":      {"report"},
	"وی‌پی‌ان":   {"vpn"},
	"ویپیان":     {"vpn"},
	"دامنه":      {"dns", "domain"},
	"ترافیک":     {"traffic", "ingress", "load"},
	"لودبالانسر": {"load", "balancer"},
	"پایپلاین":   {"pipeline", "ci"},
	"تست":        {"test"},
	"هزینه":      {"cost"},
	"ترافیکی":    {"traffic"},
	"گیت":        {"git"},
	"مخزن":       {"repository", "git"},
	"انسیبل":     {"ansible", "playbook"},
	"ترافورم":    {"terraform"},
	"هلم":        {"helm"},
	"سرویس":      {"service"},
	"ریستارت":    {"restart"},
	"راه‌اندازی": {"setup", "bootstrap", "install"},
}

// expand adds the English words of Persian ones.
func expand(terms []string) []string {
	var out []string
	for _, t := range terms {
		words := vocab[t]
		// Persian plurals and suffixes: ها، های، ی
		for _, suf := range []string{"های", "ها", "ی"} {
			if base, ok := cutSuffix(t, suf); ok {
				words = append(words, vocab[base]...)
			}
		}
		for _, e := range words {
			out = append(out, stem(e))
		}
	}
	return out
}

func cutSuffix(s, suf string) (string, bool) {
	if len(s) > len(suf)+2 && s[len(s)-len(suf):] == suf {
		return s[:len(s)-len(suf)], true
	}
	return "", false
}
