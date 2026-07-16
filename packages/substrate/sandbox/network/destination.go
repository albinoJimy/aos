package network

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Destination é o alvo de rede de uma tentativa de egress: um host (nome), um IP
// literal e uma porta. A filtragem é ao nível de IP/porta/host (DNS é AOS-068). Um
// destino é VÁLIDO para avaliação apenas se tiver uma porta > 0 E pelo menos um
// localizador (Host ou IP); caso contrário é fail-closed (a jurisdição de rede é
// indefinida ⇒ deny).
type Destination struct {
	// Host é o nome do destino (ex.: "api.github.com"). Correspondência EXACTA
	// (case-insensitive) contra os hosts da allowlist. Vazio quando o alvo é um IP
	// literal.
	Host string
	// IP é o endereço literal do destino (ex.: "93.184.216.34"). Correspondência por
	// pertença a um CIDR da allowlist. Vazio quando o alvo é um nome.
	IP string
	// Port é a porta TCP/UDP do destino (obrigatória, > 0).
	Port int
}

// valid indica se o destino pode ser avaliado (fail-closed): porta > 0 e pelo menos
// um localizador de rede. Um destino sem porta ou sem host/IP não é uma fronteira de
// egress avaliável e é sempre negado.
func (d Destination) valid() bool {
	if d.Port <= 0 || d.Port > 65535 {
		return false
	}
	return d.Host != "" || d.IP != ""
}

// String devolve uma forma canónica e NÃO-SECRETA do destino para o audit/span
// (ex.: "api.github.com:443" ou "93.184.216.34:443"). O destino não é segredo.
func (d Destination) String() string {
	loc := d.Host
	if loc == "" {
		loc = d.IP
	}
	return loc + ":" + strconv.Itoa(d.Port)
}

// NewDestination normaliza um host/IP e uma porta num [Destination]. Um host que
// seja um IP literal é colocado em IP (correspondência por CIDR); caso contrário em
// Host (correspondência exacta). O host é normalizado para minúsculas (nomes DNS são
// case-insensitive) e sem o ponto final.
func NewDestination(hostOrIP string, port int) Destination {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostOrIP), "."))
	if h == "" {
		return Destination{Port: port}
	}
	if ip := net.ParseIP(h); ip != nil {
		return Destination{IP: h, Port: port}
	}
	return Destination{Host: h, Port: port}
}

// networkResourceTypes são os tipos de [referencemonitor.Resource] que representam
// um egress de REDE (competência deste filtro). Outros tipos (ex.: "file", "db")
// NÃO são egress de rede — o hook de egress abstém-se (allow) e deixa-os para os
// outros pontos de decisão da cadeia.
var networkResourceTypes = map[string]struct{}{
	"net":    {},
	"url":    {},
	"host":   {},
	"egress": {},
}

// networkCapabilityPrefixes são os prefixos de [referencemonitor.Call.Capability] que
// denotam uma acção de EGRESS de rede (ex.: "cap:http.post", "cap:net.connect"). Um
// call que declara uma destas capabilities EXERCE egress de rede — a competência do
// hook não pode depender só do Resource.Type (que o chamador controla e pode
// omitir/mislabelar). Ver [IsNetworkCapability] e o gate fail-closed em
// [EgressHook.Evaluate]: capability de rede + Resource.Type não-rede ⇒ egress
// não-verificável ⇒ DENY (fecha o vector de exfiltração via tool com tipo mislabelado).
var networkCapabilityPrefixes = []string{"cap:http", "cap:net"}

// IsNetworkCapability indica se a capability declarada por um call é uma capability de
// EGRESS de rede (cap:http.*, cap:net.*, ou os próprios "cap:http"/"cap:net"). É
// case-insensitive e tolera espaços. Usada pelo [EgressHook] para NÃO se abster quando
// o Resource.Type não é de rede mas o call ainda assim exerce egress (fail-closed).
func IsNetworkCapability(capability string) bool {
	c := strings.ToLower(strings.TrimSpace(capability))
	if c == "" {
		return false
	}
	for _, p := range networkCapabilityPrefixes {
		if c == p || strings.HasPrefix(c, p+".") {
			return true
		}
	}
	return false
}

// DestinationFromResource deriva o [Destination] a partir do alvo concreto de uma
// tool call mediada ([referencemonitor.Resource]). Devolve ok=false quando o recurso
// NÃO é um egress de rede (o hook de egress não é competente — não bloqueia). Quando
// É um egress de rede mas o valor é malformado/incompleto, devolve ok=true com um
// destino INVÁLIDO: uma tentativa de egress que não se consegue interpretar é negada
// FAIL-CLOSED (nunca ok=false, que a deixaria passar).
func DestinationFromResource(r referencemonitor.Resource) (Destination, bool) {
	if _, isNet := networkResourceTypes[r.Type]; !isNet {
		return Destination{}, false
	}
	val := strings.TrimSpace(r.Value)
	if val == "" {
		return Destination{}, true // egress de rede sem alvo → inválido → deny
	}
	switch r.Type {
	case "url":
		return destinationFromURL(val), true
	default: // "net" | "host" | "egress": esperado "host:port" ou "ip:port"
		return destinationFromHostPort(val), true
	}
}

// destinationFromHostPort interpreta "host:port"/"ip:port". Um valor sem porta
// explícita fica inválido (Port 0) — fail-closed (a porta é obrigatória na
// filtragem IP/porta/host).
func destinationFromHostPort(val string) Destination {
	host, portStr, err := net.SplitHostPort(val)
	if err != nil {
		return Destination{} // sem porta explícita / malformado → inválido → deny
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return Destination{}
	}
	return NewDestination(host, port)
}

// destinationFromURL interpreta uma URL, derivando a porta do esquema quando
// ausente (443 para https, 80 para http). Esquemas sem porta por omissão conhecida
// e sem porta explícita ficam inválidos (fail-closed).
func destinationFromURL(val string) Destination {
	u, err := url.Parse(val)
	if err != nil || u.Hostname() == "" {
		return Destination{}
	}
	port := 0
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil {
			return Destination{}
		}
	} else {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = 443
		case "http":
			port = 80
		}
	}
	return NewDestination(u.Hostname(), port)
}
