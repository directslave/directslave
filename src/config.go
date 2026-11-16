package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/snowirbis/config"
)

type Configuration struct {
	cookie_sess_id  string
	cookie_auth_key string
	debug           string
	background      string
	named_conf      string
	authfile        string
	named_workdir   string
	named_format    string
	retry_time      string
	rndc_path       string
	host            string
	port            string
	ssl             string
	ssl_cert        string
	ssl_key         string
	sslport         string
	uid             string
	gid             string
	pid             string
	access_log      string
	error_log       string
	action_log      string

	// DirectDNS v4 integrated authoritative secondary options.
	// All of them are optional to keep legacy DirectSlave configurations valid.
	dns_server           string
	dns_server_log       string
	dns_query_log        string
	dns_query_log_file   string
	dns_query_log_buffer int
	dns_listen_ip4       string
	dns_listen_ip6       string
	dns_listen_ip4_set   bool
	dns_listen_ip6_set   bool
	dns_listen_legacy    string
	dns_port             string
	dns_new_refresh      int
	dns_new_retry        int
	dns_new_expire       int
	dns_expunge_expired  int
	transfer_queue       int
	pprof_enable         string
	pprof_port           string

	// Startup consistency check for zones restored from local storage.
	startup_check         string
	startup_check_workers int
	startup_check_rate    int
	startup_check_grace   int
}

var Config = Configuration{}

func ReadConfigFile(cfile string) (Config Configuration) {
	file, err := os.Open(cfile)
	crashError(err)
	defer file.Close()

	conf, err := config.Parse(file)
	crashError(err)

	// Legacy DirectSlave options remain mandatory and are read exactly as before.
	Config.debug, err = conf.String("debug")
	crashError(err)
	Config.background, err = conf.String("background")
	crashError(err)
	Config.named_conf, err = conf.String("named_conf")
	crashError(err)
	Config.authfile, err = conf.String("authfile")
	crashError(err)
	Config.named_workdir, err = conf.String("named_workdir")
	crashError(err)
	Config.named_format, err = conf.String("named_format")
	crashError(err)
	Config.retry_time, err = conf.String("retry_time")
	crashError(err)
	Config.rndc_path, err = conf.String("rndc_path")
	crashError(err)
	Config.host, err = conf.String("host")
	crashError(err)
	Config.port, err = conf.String("port")
	crashError(err)
	Config.ssl, err = conf.String("ssl")
	crashError(err)

	if Config.ssl == "on" {
		Config.ssl_cert, err = conf.String("ssl_cert")
		crashError(err)
		Config.ssl_key, err = conf.String("ssl_key")
		crashError(err)
		Config.sslport, err = conf.String("sslport")
		crashError(err)
	}

	Config.cookie_auth_key, err = conf.String("cookie_auth_key")
	crashError(err)
	Config.cookie_sess_id, err = conf.String("cookie_sess_id")
	crashError(err)
	Config.uid, err = conf.String("uid")
	crashError(err)
	Config.gid, err = conf.String("gid")
	crashError(err)
	Config.pid, err = conf.String("pid")
	crashError(err)
	Config.access_log, err = conf.String("access_log")
	crashError(err)
	Config.error_log, err = conf.String("error_log")
	crashError(err)
	Config.action_log, err = conf.String("action_log")
	crashError(err)

	// Legacy-safe defaults. If these keys do not exist, DirectSlave behaves as v3:
	// zones are written to named_conf and an external BIND/rndc backend is used.
	Config.dns_server = "external"
	Config.dns_server_log = Config.action_log
	Config.dns_query_log = "off"
	Config.dns_query_log_buffer = 8192
	Config.dns_listen_ip4 = "*"
	Config.dns_listen_ip6 = "*"
	Config.dns_port = "53"
	Config.dns_new_refresh = 300
	Config.dns_new_retry = 60
	Config.dns_new_expire = 604800
	Config.dns_expunge_expired = 0
	Config.transfer_queue = 20
	Config.pprof_enable = "1"
	Config.pprof_port = "2223"
	Config.startup_check = "on"
	Config.startup_check_workers = 20
	Config.startup_check_rate = 20
	Config.startup_check_grace = 0

	// Use the old BIND retry setting as the default retry timer when it is numeric.
	if v, e := strconv.Atoi(Config.retry_time); e == nil && v > 0 {
		Config.dns_new_retry = v
	}

	if v, e := conf.String("dns_server"); e == nil && strings.TrimSpace(v) != "" {
		Config.dns_server = strings.ToLower(strings.TrimSpace(v))
	}
	if v, e := conf.String("dns_server_log"); e == nil && strings.TrimSpace(v) != "" {
		Config.dns_server_log = strings.TrimSpace(v)
	}
	// Query logging is separate from the DNS event log and disabled by default.
	// If enabled without an explicit file, keep it next to dns_server_log.
	Config.dns_query_log_file = filepath.Join(filepath.Dir(Config.dns_server_log), "dns-query.log")
	if v, e := conf.String("dns_query_log"); e == nil && strings.TrimSpace(v) != "" {
		Config.dns_query_log = strings.ToLower(strings.TrimSpace(v))
	}
	if v, e := conf.String("dns_query_log_file"); e == nil && strings.TrimSpace(v) != "" {
		Config.dns_query_log_file = strings.TrimSpace(v)
	}
	if v, e := conf.String("dns_query_log_buffer"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n > 0 {
			Config.dns_query_log_buffer = n
		}
	}
	// Listen addresses, one directive per family. The old single "dns_listen"
	// is still honoured when neither new directive is present, so an upgrade
	// does not quietly move the server onto other addresses.
	if v, e := conf.String("dns_listen_ip4"); e == nil && strings.TrimSpace(v) != "" {
		Config.dns_listen_ip4 = strings.TrimSpace(v)
		Config.dns_listen_ip4_set = true
	}
	if v, e := conf.String("dns_listen_ip6"); e == nil && strings.TrimSpace(v) != "" {
		Config.dns_listen_ip6 = strings.TrimSpace(v)
		Config.dns_listen_ip6_set = true
	}
	// Naming addresses for one family means "listen here", not "here and on
	// every address of the other family": the family left out stays off.
	if Config.dns_listen_ip4_set && Config.dns_listen_ip6_set == false {
		Config.dns_listen_ip6 = "none"
	}
	if Config.dns_listen_ip6_set && Config.dns_listen_ip4_set == false {
		Config.dns_listen_ip4 = "none"
	}
	if Config.dns_listen_ip4_set == false && Config.dns_listen_ip6_set == false {
		if v, e := conf.String("dns_listen"); e == nil && strings.TrimSpace(v) != "" {
			Config.dns_listen_legacy = strings.TrimSpace(v)
			Config.dns_listen_ip4, Config.dns_listen_ip6 = splitLegacyDNSListen(Config.dns_listen_legacy)
			// "*" was the default anyway; explicit addresses were chosen.
			explicit := Config.dns_listen_legacy != "*"
			Config.dns_listen_ip4_set = explicit
			Config.dns_listen_ip6_set = explicit
		}
	}
	if v, e := conf.String("dns_port"); e == nil && strings.TrimSpace(v) != "" {
		Config.dns_port = strings.TrimSpace(v)
	}
	if v, e := conf.String("dns_new_refresh"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n > 0 {
			Config.dns_new_refresh = n
		}
	}
	if v, e := conf.String("dns_new_retry"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n > 0 {
			Config.dns_new_retry = n
		}
	}
	if v, e := conf.String("dns_new_expire"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n > 0 {
			Config.dns_new_expire = n
		}
	}
	if v, e := conf.String("dns_expunge_expired"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n >= 0 {
			Config.dns_expunge_expired = n
		}
	}
	if v, e := conf.String("transfer_queue"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n > 0 {
			Config.transfer_queue = n
		}
	}
	if v, e := conf.String("pprof_enable"); e == nil && strings.TrimSpace(v) != "" {
		Config.pprof_enable = strings.TrimSpace(v)
	}
	if v, e := conf.String("pprof_port"); e == nil && strings.TrimSpace(v) != "" {
		Config.pprof_port = strings.TrimSpace(v)
	}
	if v, e := conf.String("startup_check"); e == nil && strings.TrimSpace(v) != "" {
		Config.startup_check = strings.ToLower(strings.TrimSpace(v))
	}
	if v, e := conf.String("startup_check_workers"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n > 0 {
			Config.startup_check_workers = n
		}
	}
	if v, e := conf.String("startup_check_rate"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n > 0 {
			Config.startup_check_rate = n
		}
	}
	if v, e := conf.String("startup_check_grace"); e == nil {
		if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil && n >= 0 {
			Config.startup_check_grace = n
		}
	}

	return Config
}

// startupCheckEnabled reports whether zones restored from local storage must be
// confirmed against their primary before they are answered. It only applies to
// the integrated DNS server; the external BIND backend keeps its own state.
func startupCheckEnabled() bool {
	if integratedDNSEnabled() == false {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(Config.startup_check)) {
	case "off", "0", "no", "false":
		return false
	default:
		return true
	}
}

func integratedDNSEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(Config.dns_server)) {
	case "internal", "integrated", "directdns":
		return true
	default:
		return false
	}
}
