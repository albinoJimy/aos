package vault

// Cliente Vault REAL de LEITURA de segredos downstream (AOS-070/AOS-264) — motor
// KV v2 do HashiCorp Vault, falado pela API HTTP com SÓ a stdlib.
//
// PORQUÊ AQUI (e não noutro pacote): a invariante rainha do broker é imposta pelo
// TIPO — [Secret] transporta o valor num campo NÃO-EXPORTADO e não há construtor
// exportado. Um cliente real que devolva um [Secret] NÃO-VAZIO só se pode construir
// DENTRO deste pacote (é a fronteira onde o campo se preenche). Por isso o cliente
// real vive aqui, ao lado do [Memory] de referência, e não em cmd/aos.
//
// ZERO-DEP (ADR-017 / Carta §4.1): fala com o Vault pela API HTTP usando apenas a
// stdlib net/http — NÃO importa o SDK Go do Vault. É a mesma disciplina de
// packages/cmd/aos/vaultkeyvault.go (a custódia da KEK), cujo transporte (do()/
// ready()) serviu de molde — copiado, não partilhado, porque aquele é `package
// main` e usa o motor Transit (que RECUSA devolver material, o oposto do que o
// broker precisa).
//
// -----------------------------------------------------------------------------
// DECISÃO REGISTADA — KV v2 vs DYNAMIC SECRETS (só uma foi construída)
// -----------------------------------------------------------------------------
//
// Escolhido KV v2 (leitura de segredo ESTÁTICO pré-aprovisionado) para a v1.
// Justificação, e — sobretudo — o que isto NÃO dá:
//
//   - A v1 é IN-PROCESS (decisão D8): o broker entrega o segredo server-side ao
//     ponto de aquisição que o injecta. O CORTE de acesso que a v1 precisa já existe
//     na fronteira de INJECÇÃO — a lease tem TTL curto e é revogável, e
//     [Lease.injectInto] recusa entregar o valor de uma lease expirada/revogada.
//     Para in-process, KV v2 basta: revogar a lease corta a injecção.
//   - KV v2 é o motor mais simples e universal (qualquer segredo pré-aprovisionado),
//     sem exigir engines por-provider (database/aws/gcp/…) montados e configurados.
//
// O QUE SÓ DYNAMIC SECRETS DÃO, e KV v2 NÃO: o CORTE DOWNSTREAM REAL. Com KV v2 o
// segredo é ESTÁTICO — [Broker.Revoke] e o TTL cortam o HANDLE/lease (a injecção
// in-process), mas a credencial a jusante continua VÁLIDA no provedor externo até
// alguém a rodar fora do Vault. Só dynamic secrets (o Vault emite uma credencial
// efémera com lease_id e revoga-a no provedor via sys/leases/revoke) dão corte a
// jusante. Isso é o desenho-alvo para quando a injecção no executor REMOTO ligar
// (D8-B, declaradamente deferido) e houver corte downstream a fazer — NÃO é
// construído aqui. É coerente com ADR-006 (:52 «cada emissão é um lease revogável»,
// :86 «TTL curto REDUZ a janela»): a v1 REDUZ a janela; o corte a jusante é dynamic.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrKVConfig — o cliente KV v2 foi pedido com configuração inválida (addr/token
// em falta). Fail-closed na construção: sem endereço/credencial não se constrói um
// cliente que devolveria material.
var ErrKVConfig = errors.New("vault: config do cliente KV v2 invalida (addr e token sao obrigatorios)")

// ErrKVFetch — falha de transporte/estado ao ler o segredo no Vault (rede, HTTP
// >=400 que não seja 404, ou resposta ilegível). Fail-closed: o broker NÃO inventa
// nem substitui por outra credencial; uma leitura falhada não emite handle. O valor
// NUNCA entra na mensagem de erro.
var ErrKVFetch = errors.New("vault: falha a ler segredo no Vault KV v2")

// redactURL devolve a forma PUBLICÁVEL de um endereço: esquema, host e porta, e mais nada.
//
// AOS-337 — PORQUE EXISTE UMA CÓPIA AQUI. O nó já tem `integration.RedactURL`, e este pacote
// NÃO PODE importá-lo: `packages/integration` é o composition-root e a fronteira do ADR-019
// corre no sentido contrário (`control-plane → kernel → platform/substrate`). A duplicação é
// DELIBERADA e é a mesma decisão que o cabeçalho deste ficheiro já regista para o transporte —
// «copiado, não partilhado» —, aqui por direcção de camada em vez de por `package main`.
//
// Descarta user-info, caminho, query e fragmento — tudo o que possa carregar segredo — e
// devolve `(inválido)` para o que não se souber analisar, NUNCA o valor original: um endereço
// que o parser recusou não se sabe redigir, e é precisamente aí que uma credencial mal escapada
// tem mais probabilidade de estar.
func redactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(inválido)"
	}
	// Composto a partir dos DOIS campos que se querem preservar, em vez de concatenado à mão:
	// um endereço sem esquema continua re-analisável como a mesma coisa que entrou.
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// erroRedigido devolve um erro de TRANSPORTE com o endereço redigido.
//
// O `net/http` já redige a SENHA nos `*url.Error` que constrói (`http://admin:***@host/…`), mas
// deixa o UTILIZADOR intacto — e numa URL de Vault é ele que identifica o principal. Estes erros
// sobem ao `/readyz` e ao banner de prontidão do nó, pelo que a postura tem de ser a mesma nos
// dois sítios (AOS-337, no molde de `erroVaultRedigido` em `cmd/aos/vaultkeyvault.go`).
//
// Preserva o `Op` e a CAUSA — que é o que diagnostica: DNS, recusa de ligação, TLS — e troca só
// o endereço. Um erro que não seja `*url.Error` passa tal-qual: não se inventa redacção sobre
// uma forma que não se conhece.
func erroRedigido(addr string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %w", ue.Op, redactURL(addr), ue.Err)
	}
	return err
}

// defaultKVMount / defaultKVField são os defaults do motor KV v2 do Vault.
const (
	defaultKVMount = "secret" // mount canónico do KV v2 num Vault dev/single-node
	defaultKVField = "value"  // campo que carrega o valor do segredo dentro do path
)

// KVv2Config parametriza o cliente KV v2. Addr e Token são obrigatórios.
type KVv2Config struct {
	// Addr é a raiz do Vault (ex.: "https://vault:8200"). https em produção; http
	// só em dev/loopback (a validação de esquema é de quem chama, no molde do nó).
	Addr string
	// Token é o token do Vault (lido de FICHEIRO montado por quem chama — material
	// privado NUNCA por variável de ambiente). Nunca é logado.
	Token string
	// Mount é o mount do motor KV v2 (default "secret").
	Mount string
	// Prefix é um prefixo de path OPCIONAL para dar namespace às credenciais do
	// broker dentro do KV (ex.: "aos-broker"); vazio ⇒ sem prefixo.
	Prefix string
	// Field é o nome do campo, dentro do segredo KV, que carrega o valor downstream
	// (default "value"). Todos os campos do path que não este são ignorados.
	Field string
	// HTTPClient permite injectar o transporte (seam de teste/egress, como no nó).
	// nil ⇒ um cliente com timeout curto por omissão.
	HTTPClient *http.Client
}

// KVv2 é o cliente Vault REAL (motor KV v2). Lê o segredo downstream server-side e
// encapsula-o num [Secret] opaco — cujo valor nem este pacote volta a ler, só
// reencaminha para um [Sink]. Sem estado mutável; o http.Client é concorrente-seguro.
type KVv2 struct {
	addr   string
	mount  string
	prefix string
	field  string
	token  string
	hc     *http.Client
}

// verifica em compile-time que o cliente real satisfaz a porta do Vault.
var _ Client = (*KVv2)(nil)

// NewKVv2 constrói o cliente KV v2. Fail-closed: addr/token vazios ⇒ [ErrKVConfig].
func NewKVv2(cfg KVv2Config) (*KVv2, error) {
	addr := strings.TrimRight(strings.TrimSpace(cfg.Addr), "/")
	token := strings.TrimSpace(cfg.Token)
	if addr == "" || token == "" {
		return nil, ErrKVConfig
	}
	mount := strings.Trim(strings.TrimSpace(cfg.Mount), "/")
	if mount == "" {
		mount = defaultKVMount
	}
	field := strings.TrimSpace(cfg.Field)
	if field == "" {
		field = defaultKVField
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &KVv2{
		addr:   addr,
		mount:  mount,
		prefix: strings.Trim(strings.TrimSpace(cfg.Prefix), "/"),
		field:  field,
		token:  token,
		hc:     hc,
	}, nil
}

// secretPath deriva o path do segredo (dentro do mount) a partir da [Key]. Layout
// DOCUMENTADO e determinista: [prefix/]provider/region/capability, com cada segmento
// SANITIZADO para caracteres Vault/URL-seguros (a capability traz ':' e '.', ex.:
// "cap:http.post"). O operador aprovisiona o segredo neste path com o campo `field`.
func (c *KVv2) secretPath(key Key) string {
	segs := make([]string, 0, 4)
	if c.prefix != "" {
		segs = append(segs, c.prefix)
	}
	segs = append(segs, sanitizeSegment(key.Provider), sanitizeSegment(key.Region), sanitizeSegment(key.Capability))
	return strings.Join(segs, "/")
}

// sanitizeSegment reduz um segmento a [A-Za-z0-9._-], substituindo o resto por '_'.
// Determinista: o aprovisionamento server-side aplica a mesma regra ao escolher o
// path. Um segmento vazio vira "_" para não colapsar a estrutura do path.
func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// kvReadResponse é a forma da resposta de leitura KV v2: o valor vive em
// data.data[field] (o duplo `data` é o envelope do KV v2 — dados + metadados).
type kvReadResponse struct {
	Data struct {
		Data map[string]string `json:"data"`
	} `json:"data"`
}

// Fetch implementa [Client]: lê o segredo no Vault (GET .../v1/{mount}/data/{path})
// e devolve-o encapsulado num [Secret] opaco construído AQUI. Fail-closed:
//
//   - 404 (path inexistente) ou campo ausente/vazio ⇒ [ErrNoMaterial] (o broker não
//     inventa credencial);
//   - erro de transporte / HTTP >=400 (≠404) / resposta ilegível ⇒ [ErrKVFetch].
//
// O valor NUNCA é logado nem entra no erro; só existe encapsulado no [Secret].
func (c *KVv2) Fetch(ctx context.Context, key Key) (Secret, error) {
	path := c.secretPath(key)
	// PathEscape por segmento preserva o '/' estrutural do KV e escapa o resto.
	escaped := make([]string, 0)
	for _, seg := range strings.Split(path, "/") {
		escaped = append(escaped, url.PathEscape(seg))
	}
	endpoint := c.addr + "/v1/" + c.mount + "/data/" + strings.Join(escaped, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		// NÃO se ecoa o erro: é um `*url.Error` que traz o endereço COMO FOI ESCRITO,
		// sem a redacção que o `http.Client` aplica aos SEUS erros. Era aqui e no ramo
		// equivalente de [KVv2.Ready] que uma senha embutida ia INTEIRA para a mensagem
		// (AOS-337).
		return Secret{}, fmt.Errorf("%w: o endereco %s nao produz um pedido HTTP valido", ErrKVFetch, redactURL(c.addr))
	}
	req.Header.Set("X-Vault-Token", c.token) // nunca logado

	resp, err := c.hc.Do(req)
	if err != nil {
		return Secret{}, fmt.Errorf("%w: transporte: %v", ErrKVFetch, erroRedigido(c.addr, err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Secret{}, ErrNoMaterial // fail-closed: sem material para a chave
	case resp.StatusCode != http.StatusOK:
		return Secret{}, fmt.Errorf("%w: status %d", ErrKVFetch, resp.StatusCode)
	}

	var out kvReadResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return Secret{}, fmt.Errorf("%w: resposta ilegivel", ErrKVFetch)
	}
	value := out.Data.Data[c.field]
	if value == "" {
		return Secret{}, ErrNoMaterial // campo ausente/vazio ⇒ sem material
	}
	// Secret construído DENTRO do pacote: ref NÃO-SECRETO (o mesmo id do Memory,
	// para redacção/auditoria consistentes), valor no campo não-exportado.
	return Secret{ref: key.id(), v: value}, nil
}

// Ready sonda a saúde do Vault para o /readyz e para o banner (molde de
// vaultkeyvault.ready): /v1/sys/seal-status é NÃO-AUTENTICADO e responde 200
// sempre; o campo `sealed` distingue pronto de selado. Um Vault inalcançável ou
// selado não serve credenciais downstream. Não devolve o token nem o valor.
func (c *KVv2) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.addr+"/v1/sys/seal-status", nil)
	if err != nil {
		// Devolvia o `*url.Error` CRU — sem redacção E sem sentinela, pelo que o /readyz
		// recebia o endereço inteiro num erro que nem era atribuível (AOS-337).
		return fmt.Errorf("%w: o endereco %s nao produz um pedido HTTP valido", ErrKVFetch, redactURL(c.addr))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: vault inalcancavel: %v", ErrKVFetch, erroRedigido(c.addr, err))
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: seal-status HTTP %d", ErrKVFetch, resp.StatusCode)
	}
	if !strings.Contains(string(rb), `"sealed":false`) {
		return fmt.Errorf("%w: vault selado", ErrKVFetch)
	}
	return nil
}
