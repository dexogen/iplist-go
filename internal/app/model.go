package app

type Site struct {
	Name          string         `json:"name"`
	Group         string         `json:"group"`
	Domains       []string       `json:"domains"`
	IP4           []string       `json:"ip4"`
	IP6           []string       `json:"ip6"`
	CIDR4         []string       `json:"cidr4"`
	CIDR6         []string       `json:"cidr6"`
	Icon          string         `json:"icon"`
	DynamicCounts map[string]int `json:"dynamicCounts,omitempty"`
	DynamicTotal  int            `json:"dynamicTotal,omitempty"`
	DNS           []string       `json:"-"`
	Timeout       int            `json:"-"`
	Replace       replaceConfig  `json:"-"`
}

type SiteSummary struct {
	Name          string         `json:"name"`
	Group         string         `json:"group"`
	Icon          string         `json:"icon"`
	Counts        map[string]int `json:"counts"`
	DynamicCounts map[string]int `json:"dynamicCounts,omitempty"`
	DynamicTotal  int            `json:"dynamicTotal,omitempty"`
}

type GroupSummary struct {
	Name  string        `json:"name"`
	Sites []SiteSummary `json:"sites"`
}

type ConfigSetInfo struct {
	Key       string `json:"key"`
	Dir       string `json:"dir"`
	Label     string `json:"label"`
	URL       string `json:"url"`
	APIBase   string `json:"apiBase"`
	IsDefault bool   `json:"isDefault"`
}

type AllData struct {
	Sets          map[string]*ConfigSetData
	SetOrder      []string
	SetInfos      []ConfigSetInfo
	Icons         map[string]string
	Runtime       *DNSRuntimeStore
	ExportCache   *ExportCache
	Sources       map[string]SnapshotDescriptor
	Revision      string
	RefreshStatus func() SnapshotRefreshStatus
	DNSStatus     func(string) DNSRefreshStatus
	MergedSets    map[string]*ConfigSetData
}

type ConfigSetData struct {
	ConfigSet string
	Sites     []Site
	Groups    []GroupSummary
}

type siteConfig struct {
	Domains []string      `json:"domains"`
	DNS     []string      `json:"dns"`
	Timeout int           `json:"timeout"`
	IP4     []string      `json:"ip4"`
	IP6     []string      `json:"ip6"`
	CIDR4   []string      `json:"cidr4"`
	CIDR6   []string      `json:"cidr6"`
	Replace replaceConfig `json:"replace"`
}

type replaceConfig struct {
	CIDR4 map[string][]string `json:"cidr4"`
	CIDR6 map[string][]string `json:"cidr6"`
}
