package main

// Custódia da KEK por-titular em HashiCorp Vault (Transit), pela porta [audit.KeyWrapper]
// (AOS-216, residual de DEF-302/AOS-215). É a alternativa REAL ao vault in-memory de referência: a
// KEK do titular vive DENTRO do Vault e NUNCA entra no processo do nó — o embrulho/desembrulho da
// DEK corre no Vault (Transit encrypt/decrypt); o crypto-shred (GDPR Art. 17) é a DESTRUIÇÃO da
// chave Transit do titular, após a qual o UnwrapDEK falha fechado e o conteúdo é irrecuperável.
//
// ZERO-DEP (ADR-017/Carta §4.1): fala com o Vault pela API HTTP usando SÓ a stdlib — não importa o
// SDK Go do Vault. É a mesma disciplina de todos os adaptadores de porta do nó; o go.mod não é
// tocado. O comentário de [audit.KeyWrapper] nomeia explicitamente "HashiCorp Vault Transit" como
// backing sancionado desta porta.
//
// DEV vs PRODUÇÃO: aqui o token do Vault é lido de um FICHEIRO montado (material privado nunca por
// variável de ambiente, no padrão de AOS_ISSUER_KEY_PATH); em produção o transporte é https e o
// token vem de um AppRole/Kubernetes-auth de curta duração. O contrato de custódia (key-never-leaves
// + shred por destruição de chave) é o mesmo.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aos-ref/integration"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/redaction"
)

// ErrVaultKEK — falha ao provisionar/embrulhar contra o Vault (WrapDEK/EnsureKey). Fail-closed: a
// ingestão de um registo com PII propaga o erro pela cadeia de cifra; NUNCA há fallback silencioso
// para custódia mais fraca.
var ErrVaultKEK = errors.New("aos: custódia da KEK no Vault falhou")

// ErrVaultToken — o TOKEN do nó no Vault está inválido, ilegível ou perto de expirar (AOS-249/F6).
// É a diferença entre "o Vault está bem" e "a NOSSA credencial ainda serve": o /v1/sys/seal-status
// que a sonda já usava é NÃO-AUTENTICADO e responde 200 a um nó cujo token expirou há horas. Sem
// esta distinção, um token de curta duração — o que o próprio README recomenda em produção —
// matava a custódia da KEK em SILÊNCIO, com o /readyz verde a mandar tráfego para um nó que já não
// consegue embrulhar DEKs nem fazer crypto-shred.
var ErrVaultToken = errors.New("aos: token do Vault (custódia da KEK) INVÁLIDO ou a expirar — o embrulho da DEK e o crypto-shred/DSAR ficam inoperantes")

// ErrVaultShredUnconfirmed — o crypto-shred correu mas a destruição da chave Transit NÃO foi
// confirmada (a chave ainda responde, ou a verificação foi recusada). Fail-closed de custódia: um
// /dsar/erase ou uma expiração de retenção que o nó dá por feita sem a chave ter morrido é uma
// afirmação FALSA de irrecuperabilidade perante o titular — pior do que não apagar, porque é
// credível. Enquanto houver uma destruição por confirmar, a sonda de prontidão fica VERMELHA.
var ErrVaultShredUnconfirmed = errors.New("aos: crypto-shred NÃO CONFIRMADO no Vault — a KEK do titular pode continuar viva e o conteúdo recuperável")

// DefaultVaultTokenMinTTL é a MARGEM de segurança do token: a sonda de prontidão fica vermelha
// quando o TTL restante desce abaixo dela — ou seja, ANTES da expiração, não depois. Cinco minutos
// dão ao orquestrador tempo para drenar o nó e ao operador tempo para reagir, e são folgados face
// ao período de renovação (que é uma fracção disto).
const DefaultVaultTokenMinTTL = 5 * time.Minute

// vaultTokenLookupCacheTTL é a validade do último `lookup-self` para efeitos da sonda. Sem cache, o
// /readyz (sondado de segundos a segundos pelo orquestrador) faria um pedido autenticado ao Vault
// por probe. Com ela, a sonda usa o TTL conhecido a DECAIR com o relógio — o veredicto continua a
// ficar vermelho na margem certa mesmo que o Vault não seja tocado nesse instante.
const vaultTokenLookupCacheTTL = 15 * time.Second

// vaultKeyVault implementa [audit.KeyWrapper] (+ [audit.KeyVault]) sobre o motor Transit do Vault.
// A KEK por-titular é uma chave Transit nomeada de forma DETERMINÍSTICA e SEM PII (sha256 do
// keyRef); a KEK crua nunca é devolvida ao nó.
type vaultKeyVault struct {
	addr  string // ex.: "https://vault:8200" (prod, via SSL_CERT_FILE) ou http em loopback (dev)
	mount string // mount do motor Transit (default "transit")
	hc    *http.Client

	// --- AOS-249: estado do TOKEN e da custódia, sob mu ---
	// Lido pela sonda de prontidão (/readyz, /metrics) e escrito pelo renovador em background:
	// o mutex é o que torna a troca de token segura a meio de um pedido em voo.
	mu sync.RWMutex
	// token é o token do Vault em vigor. NUNCA é logado nem entra numa mensagem de erro — só o
	// CAMINHO do ficheiro e o veredicto (válido/expirado) são observáveis.
	token string
	// tokenPath é o ficheiro montado de onde o token veio. Não-vazio ⇒ o renovador RE-LÊ o
	// ficheiro a cada tick, que é como um token rodado por um agente externo (AppRole/
	// Kubernetes-auth a escrever no mesmo caminho) é adoptado sem reiniciar o nó nem acrescentar
	// uma dependência. Vazio ⇒ token estático (só dev/testes).
	tokenPath string
	// minTTL é a margem abaixo da qual o token é considerado NÃO-SERVÍVEL para a prontidão.
	minTTL time.Duration
	// tokenTTL/tokenSeen são o último `lookup-self` bem-sucedido: TTL restante NESSE instante e o
	// instante. O restante em qualquer momento é tokenTTL - (agora - tokenSeen). tokenTTL == 0
	// com tokenSeen não-zero significa token SEM expiração (root/periódico).
	tokenTTL  time.Duration
	tokenSeen time.Time
	// tokenErr é o último veredicto negativo do token (nil = servível). Sticky até um lookup
	// bem-sucedido o limpar.
	tokenErr error
	// tokenOpaco: a credencial FUNCIONA (prova de capacidade passou) mas a política não deixa
	// medir o TTL nem renovar. Serve, e tem uma data de morte que ninguém vê chegar.
	tokenOpaco   bool
	opacoAvisado bool
	// refreshing é a porta de SINGLEFLIGHT do refresh conduzido pela SONDA (ver [tokenHealth]).
	// Fora do mutex de propósito: é um gate de admissão, não estado partilhado — o refresh que
	// ganha o CAS segura o `mu` só nas escritas de estado, e quem perde não bloqueia.
	refreshing atomic.Bool
	// shredPend são as chaves Transit cuja DESTRUIÇÃO ficou por confirmar (nomes já derivados por
	// sha256, sem PII). Não-vazio ⇒ a sonda de prontidão fica vermelha; uma destruição posterior
	// CONFIRMADA da mesma chave limpa a entrada (o operador pode remediar sem reiniciar).
	shredPend map[string]struct{}
}

// vaultKeyVaultOption configura o adaptador na construção (variádica para não partir os
// chamadores de três argumentos que já existem).
type vaultKeyVaultOption func(*vaultKeyVault)

// withVaultTokenFile declara o ficheiro montado de onde o token veio, habilitando a RE-LEITURA
// periódica (adopção de um token rodado por um agente externo). Caminho vazio ⇒ sem re-leitura.
func withVaultTokenFile(path string) vaultKeyVaultOption {
	return func(v *vaultKeyVault) { v.tokenPath = strings.TrimSpace(path) }
}

// withVaultTokenMinTTL sobrepõe a margem de prontidão do token. <= 0 mantém o default.
func withVaultTokenMinTTL(d time.Duration) vaultKeyVaultOption {
	return func(v *vaultKeyVault) {
		if d > 0 {
			v.minTTL = d
		}
	}
}

// newVaultKeyVault constrói o adaptador. addr/mount/token já validados por quem chama (o esquema
// de addr é validado em [parseVaultDSARFromEnv] com o critério partilhado
// [integration.CheckSecureTransportURL]).
func newVaultKeyVault(addr, mount, token string, opts ...vaultKeyVaultOption) *vaultKeyVault {
	v := &vaultKeyVault{
		addr:      strings.TrimRight(addr, "/"),
		mount:     strings.Trim(mount, "/"),
		token:     token,
		minTTL:    DefaultVaultTokenMinTTL,
		shredPend: make(map[string]struct{}),
		hc:        &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// currentToken devolve o token em vigor sob lock de leitura. NUNCA logar o retorno.
func (v *vaultKeyVault) currentToken() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.token
}

// vaultKeyName mapeia o keyRef da camada de auditoria (KeyRefFor(subjectID) = prefixo+subjectID,
// que pode conter ':' e não é um nome de chave Transit válido) para um nome DETERMINÍSTICO e
// Vault-safe. O sha256 também impede que o subjectID (potencial PII) apareça no nome da chave.
func vaultKeyName(keyRef string) string {
	sum := sha256.Sum256([]byte(keyRef))
	return "aos-kek-" + hex.EncodeToString(sum[:])
}

// do executa um pedido HTTP autenticado ao Vault e devolve corpo+status. Erros de transporte e
// status >= 400 (exceto os que o chamador tolera) viram erro tipado.
func (v *vaultKeyVault) do(method, path string, body any) ([]byte, int, error) {
	return v.doCtx(context.Background(), method, path, body)
}

// doCtx é o [do] com contexto — usado pelas sondas, que trazem o timeout curto do /readyz e não
// podem herdar só o timeout do cliente.
func (v *vaultKeyVault) doCtx(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, v.addr+path, rdr)
	if err != nil {
		// O erro do `NewRequest` é um `*url.Error` que traz a URL CRUA — e, ao contrário
		// do erro de transporte, sem a redacção da senha que o `http.Client` aplica. Um
		// endereço com credenciais embutidas ia inteiro para aqui, e daqui para o
		// `/readyz` e para o banner de prontidão (AOS-333). O ambiente já não deixa
		// compor um endereço destes; este ramo é a defesa em profundidade para quem
		// compuser `Config` por outra via.
		return nil, 0, fmt.Errorf("pedido invalido para o vault %s: %w", integration.RedactURL(v.addr), errPedidoVaultInvalido)
	}
	req.Header.Set("X-Vault-Token", v.currentToken()) // nunca logado
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.hc.Do(req)
	if err != nil {
		return nil, 0, erroVaultRedigido(v.addr, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return rb, resp.StatusCode, nil
}

// ---------------------------------------------------------------------------
// AOS-249 — validade e renovação do TOKEN (a custódia não morre em silêncio)
// ---------------------------------------------------------------------------

// reloadTokenFile re-lê o ficheiro montado do token e adopta-o se MUDOU. É a via de adopção de um
// token rodado por um agente externo (AppRole/Kubernetes-auth) sem reiniciar o nó e sem uma
// dependência nova (ADR-017). Devolve true quando houve troca.
//
// O VALOR nunca é logado nem entra num erro — só o CAMINHO. Um ficheiro que desapareceu ou ficou
// vazio é erro: o operador tem de o ver, e não como um nó que continua verde com uma credencial
// que já não existe no disco.
func (v *vaultKeyVault) reloadTokenFile() (bool, error) {
	v.mu.RLock()
	path, atual := v.tokenPath, v.token
	v.mu.RUnlock()
	if path == "" {
		return false, nil // token estático (dev/testes): nada a re-ler
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("%w: ler o ficheiro do token %q: %v", ErrVaultToken, path, err)
	}
	novo := strings.TrimSpace(string(raw))
	if novo == "" {
		return false, fmt.Errorf("%w: ficheiro do token %q VAZIO", ErrVaultToken, path)
	}
	if novo == atual {
		return false, nil
	}
	v.mu.Lock()
	v.token = novo
	v.mu.Unlock()
	return true, nil
}

// errLookupNegado distingue as DUAS causas de um 403 em `lookup-self`, que o código anterior
// confundia — e a confusão custou um /readyz VERMELHO sobre um token perfeitamente bom:
//
//  1. a credencial MORREU (expirou/foi revogada) — o caso que a sonda quer apanhar;
//  2. a credencial está VIVA mas a política não lhe concede `auth/token/lookup-self`.
//
// (2) é o caso NOMINAL de um token least-privilege: `provision-identity.sh` emite-o com
// `no_default_policy: true` e uma política só sobre `transit/*`, e é a política `default` que
// concede o `lookup-self`. Ou seja: o provisionamento deste projecto produz um token que a sonda
// deste projecto não sabe interrogar.
//
// Um 403 aqui NÃO é evidência de token morto. É evidência de que ESTE canal está fechado.
var errLookupNegado = errors.New("aos: lookup-self negado pela politica do token")

// errPedidoVaultInvalido — o endereco do Vault nao produz um pedido HTTP valido.
//
// Existe para que o caminho de erro NAO devolva o `*url.Error` do `NewRequest`, que traz a
// URL CRUA (AOS-333). O `http.Client` redige a senha nos SEUS erros de transporte; o
// `NewRequest` nao redige nada, pelo que era o unico sitio do no onde um endereco com
// credenciais embutidas ia INTEIRO para o `/readyz` e para o banner de prontidao.
var errPedidoVaultInvalido = errors.New("aos: endereco do Vault nao produz um pedido HTTP valido")

// erroVaultRedigido devolve um erro de transporte com o endereco REDIGIDO.
//
// O corpo desceu a `substrate/redaction` (AOS-337): o cliente Vault do BROKER precisa da mesma
// redaccao e nao pode importar `integration` nem este pacote (ADR-019), pelo que a alternativa
// era uma terceira copia — e as duas primeiras ja tinham divergido na sentinela de invalido.
// O nome mantem-se porque e o que os chamadores deste ficheiro usam.
func erroVaultRedigido(addr string, err error) error {
	return redaction.TransportError(addr, err)
}

// lookupSelf é a PROVA de que o nosso token ainda serve: /v1/auth/token/lookup-self é AUTENTICADO
// e é o único endpoint do Vault que responde à pergunta certa — não "o Vault está vivo?" (que o
// seal-status já respondia, e responderia na mesma com o token morto) mas "esta credencial ainda
// é aceite, e por quanto tempo?". Devolve o TTL restante (0 = token sem expiração) e se é
// renovável.
func (v *vaultKeyVault) lookupSelf(ctx context.Context) (time.Duration, bool, error) {
	rb, code, err := v.doCtx(ctx, http.MethodGet, "/v1/auth/token/lookup-self", nil)
	if err != nil {
		return 0, false, fmt.Errorf("%w: lookup-self: %v", ErrVaultToken, err)
	}
	if code != http.StatusOK {
		if code == http.StatusForbidden {
			// NÃO se conclui daqui que o token morreu — ver [errLookupNegado]. Quem decide é a
			// prova de capacidade, contra a operação que o nó realmente precisa de fazer.
			return 0, false, fmt.Errorf("%w: %w (HTTP 403)", ErrVaultToken, errLookupNegado)
		}
		// Qualquer outro status não prova nada a favor da credencial.
		return 0, false, fmt.Errorf("%w: lookup-self recusado (HTTP %d)", ErrVaultToken, code)
	}
	var out struct {
		Data struct {
			TTL       int64 `json:"ttl"`
			Renewable bool  `json:"renewable"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return 0, false, fmt.Errorf("%w: resposta de lookup-self ilegível", ErrVaultToken)
	}
	if out.Data.TTL < 0 {
		return 0, false, fmt.Errorf("%w: lookup-self devolveu TTL negativo", ErrVaultToken)
	}
	return time.Duration(out.Data.TTL) * time.Second, out.Data.Renewable, nil
}

// renewSelf estende a lease do token (/v1/auth/token/renew-self, autenticado). Só é chamado num
// token renovável e com expiração; falhar aqui NÃO é fatal por si — o veredicto de prontidão sai
// sempre do TTL medido, não da tentativa de renovação.
func (v *vaultKeyVault) renewSelf(ctx context.Context) error {
	_, code, err := v.doCtx(ctx, http.MethodPost, "/v1/auth/token/renew-self", nil)
	if err != nil {
		return fmt.Errorf("%w: renew-self: %v", ErrVaultToken, err)
	}
	if code != http.StatusOK && code != http.StatusNoContent {
		return fmt.Errorf("%w: renew-self recusado (HTTP %d)", ErrVaultToken, code)
	}
	return nil
}

// refreshToken é o PONTO ÚNICO de manutenção da credencial, invocado pelo renovador em background
// (molde do varrimento de aprovações) e pela sonda quando o estado está velho:
//
//  1. re-lê o ficheiro montado (adopta um token rodado por fora);
//  2. `lookup-self` — mede a validade REAL contra o Vault;
//  3. renova (`renew-self`) quando o token é renovável e o TTL restante já entrou no dobro da
//     margem de prontidão — renova-se ANTES de a sonda ficar vermelha, e a sonda continua a ser
//     a rede de segurança se a renovação falhar.
//
// Devolve o veredicto CORRENTE do token (nil = servível). O estado interno fica actualizado em
// qualquer caso — é dele que a sonda tira a decisão.
func (v *vaultKeyVault) refreshToken(ctx context.Context) error {
	if _, err := v.reloadTokenFile(); err != nil {
		v.setTokenErr(err)
		return err
	}
	ttl, renovavel, err := v.lookupSelf(ctx)
	if err != nil {
		// O canal de MEDIÇÃO estar fechado não é o mesmo que a credencial estar morta. Antes de
		// pôr o /readyz vermelho, testa-se a PROPRIEDADE que a prontidão protege: consigo ainda
		// falar com o Transit? Se sim, o nó serve — e o que se perdeu (o horizonte) é declarado.
		if errors.Is(err, errLookupNegado) {
			if perr := v.provaDeCapacidade(ctx); perr == nil {
				v.setTokenOpaco()
				return nil
			}
		}
		v.setTokenErr(err)
		return err
	}
	v.setTokenState(ttl)

	v.mu.RLock()
	limiar := 2 * v.minTTL
	v.mu.RUnlock()
	if renovavel && ttl > 0 && ttl < limiar {
		// Uma renovação falhada NÃO é convertida em veredicto: o TTL pode continuar acima da
		// margem e, nesse caso, o nó está legitimamente pronto. O limiar é o DOBRO da margem
		// justamente para haver muitos ticks entre a primeira falha e o vermelho — e quem
		// denuncia a falha persistente é a sonda, que fica vermelha antes da expiração.
		if rerr := v.renewSelf(ctx); rerr == nil {
			if novoTTL, _, lerr := v.lookupSelf(ctx); lerr == nil {
				v.setTokenState(novoTTL)
			}
		}
	}
	return v.tokenVerdict(time.Now())
}

// setTokenState regista um lookup-self bem-sucedido (e limpa um veredicto negativo anterior).
func (v *vaultKeyVault) setTokenState(ttl time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.tokenTTL, v.tokenSeen, v.tokenErr = ttl, time.Now(), nil
	// Um lookup bem-sucedido significa que o canal de medição REABRIU (política corrigida): o
	// estado opaco levanta-se, e um aviso futuro volta a poder ser dado.
	v.tokenOpaco, v.opacoAvisado = false, false
}

// setTokenErr regista um veredicto negativo. É STICKY: só um lookup-self bem-sucedido o limpa.
func (v *vaultKeyVault) setTokenErr(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.tokenErr = err
}

// tokenVerdict decide se o token SERVE no instante `agora`, a partir do estado conhecido:
//
//   - veredicto negativo pendente (lookup recusado, ficheiro ilegível) ⇒ NÃO serve;
//   - validade nunca verificada ⇒ NÃO serve (fail-closed: um token por verificar não é um token
//     bom, é um token desconhecido);
//   - TTL == 0 num lookup bem-sucedido ⇒ token sem expiração (root/periódico) ⇒ serve;
//   - TTL restante (a decair com o relógio) abaixo da margem ⇒ NÃO serve — é ESTA cláusula que
//     torna o /readyz vermelho ANTES da expiração em vez de depois.
func (v *vaultKeyVault) tokenVerdict(agora time.Time) error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.tokenErr != nil {
		return v.tokenErr
	}
	if v.tokenSeen.IsZero() {
		return fmt.Errorf("%w: validade NUNCA verificada contra o Vault", ErrVaultToken)
	}
	if v.tokenTTL <= 0 {
		return nil // sem expiração
	}
	restante := v.tokenTTL - agora.Sub(v.tokenSeen)
	if restante < v.minTTL {
		return fmt.Errorf("%w: expira em %s (margem de prontidão %s)", ErrVaultToken,
			restante.Truncate(time.Second), v.minTTL)
	}
	return nil
}

// tokenHealth é o veredicto do token para a sonda: usa o estado conhecido enquanto está fresco e,
// quando está velho (ou negativo), vai ao Vault confirmá-lo com o timeout curto de quem sonda.
//
// SINGLEFLIGHT (remediação da W6). `/metrics` e `/readyz` são NÃO-AUTENTICADOS e não passam pelo
// token-bucket do plano de controlo; a verificação de frescura sozinha não serializa nada, pelo
// que N sondas concorrentes viam todas "não fresco" e disparavam N `lookup-self`/`renew-self`
// AUTENTICADOS contra o Vault. Um burst anónimo traduzia-se em amplificação de pedidos
// autenticados e, se o Vault respondesse com rate-limit, [setTokenErr] é STICKY: o nó ia a
// UNREADY — negação de serviço sobre a prontidão sem credencial nenhuma.
//
// A porta é NÃO-BLOQUEANTE de propósito: quem perde o CAS não espera pelo refresh em curso —
// responde com o último estado conhecido, que é exactamente o que uma sonda deve fazer (o
// veredicto decai com o relógio em [tokenVerdict], logo continua a ficar vermelho na margem
// certa). Com a cache de frescura, o custo fica limitado a um `lookup-self` por
// [vaultTokenLookupCacheTTL], qualquer que seja a taxa de sondagem.
func (v *vaultKeyVault) tokenHealth(ctx context.Context) error {
	v.mu.RLock()
	fresco := v.tokenErr == nil && !v.tokenSeen.IsZero() && time.Since(v.tokenSeen) < vaultTokenLookupCacheTTL
	v.mu.RUnlock()
	if !fresco && v.refreshing.CompareAndSwap(false, true) {
		defer v.refreshing.Store(false)
		_ = v.refreshToken(ctx) // o veredicto sai do ESTADO, não do erro devolvido
	}
	return v.tokenVerdict(time.Now())
}

// shredFault devolve a falha de crypto-shred por confirmar, se houver.
func (v *vaultKeyVault) shredFault() error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.shredPend) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d chave(s) por confirmar", ErrVaultShredUnconfirmed, len(v.shredPend))
}

// shredPending implementa [shredPendingReporter]: quantas destruições de chave continuam POR
// CONFIRMAR. É uma contagem — nunca titulares — e é o que distingue, para quem opera, este eixo
// do eixo do token (ambos põem o /readyz vermelho e o corpo do /readyz é deliberadamente
// uniforme). Ver [apiHandler.handleMetrics] e [NodeService.sealRetentionSweep].
func (v *vaultKeyVault) shredPending() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.shredPend)
}

// shredConfirmed implementa [shredConfirmer]: diz se a destruição da KEK DESTE titular ficou
// por confirmar na última tentativa. É o que permite ao sink de expiração e ao handler do
// /dsar/erase deixarem de afirmar uma irrecuperabilidade que a custódia não confirmou, sem
// mudar a assinatura de [audit.KeyVault] (que não tem canal de erro no Delete).
//
// A mensagem nomeia a CHAVE pelo seu nome derivado (sha256 do keyRef) — nunca o titular.
func (v *vaultKeyVault) shredConfirmed(subjectID string) error {
	if subjectID == "" {
		return nil
	}
	name := vaultKeyName(audit.KeyRefFor(subjectID))
	v.mu.RLock()
	defer v.mu.RUnlock()
	if _, pend := v.shredPend[name]; pend {
		return fmt.Errorf("%w: chave %s (a destruicao nao foi confirmada pelo Vault)", ErrVaultShredUnconfirmed, name)
	}
	return nil
}

// ready sonda a custódia da KEK para o /readyz. São TRÊS perguntas independentes, e falhar
// qualquer uma põe o nó UNREADY:
//
//	(a) o Vault está vivo e destravado — /v1/sys/seal-status, NÃO-AUTENTICADO (revisão #2);
//	(b) o NOSSO token ainda serve, com margem — lookup-self, AUTENTICADO (AOS-249/F6);
//	(c) nenhuma destruição de chave ficou por confirmar (AOS-249, erase/expire fail-closed).
//
// A alínea (b) é o buraco que este ticket fecha: (a) sozinha é uma sonda sobre o VIZINHO, não
// sobre a nossa capacidade de o usar. Um token de curta duração — o que o README recomenda em
// produção — expira e o seal-status continua a responder 200 `sealed:false`: o nó ficava verde a
// encaminhar tráfego enquanto cada WrapDEK e cada crypto-shred levava 403. É a morte silenciosa
// da custódia descrita em F6.
//
// O ctx traz um timeout curto de quem chama (não bloqueia o readyz).
func (v *vaultKeyVault) ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.addr+"/v1/sys/seal-status", nil)
	if err != nil {
		// Mesmo motivo do `do`: este caminho alimenta o `/readyz`, que é onde um segredo
		// no endereço seria mais visto (AOS-333).
		return fmt.Errorf("pedido invalido para o vault %s: %w", integration.RedactURL(v.addr), errPedidoVaultInvalido)
	}
	resp, err := v.hc.Do(req)
	if err != nil {
		return fmt.Errorf("vault inalcancavel: %w", erroVaultRedigido(v.addr, err))
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault seal-status HTTP %d", resp.StatusCode)
	}
	if !bytes.Contains(rb, []byte(`"sealed":false`)) {
		return errors.New("vault selado")
	}
	if err := v.tokenHealth(ctx); err != nil {
		return err
	}
	return v.shredFault()
}

// ensureTransitKey garante (idempotente) que a chave Transit do titular existe. Criar uma chave já
// existente é no-op no Vault (204).
func (v *vaultKeyVault) ensureTransitKey(name string) error {
	_, code, err := v.do(http.MethodPost, "/v1/"+v.mount+"/keys/"+name, map[string]string{"type": "aes256-gcm96"})
	if err != nil {
		return fmt.Errorf("%w: criar chave: %v", ErrVaultKEK, err)
	}
	if code != http.StatusOK && code != http.StatusNoContent {
		return fmt.Errorf("%w: criar chave: status %d", ErrVaultKEK, code)
	}
	return nil
}

// WrapDEK embrulha a DEK sob a chave Transit do titular DENTRO do Vault (encrypt). A KEK não sai.
// Devolve o ciphertext opaco do Vault ("vault:v1:...") e o keyRef da camada de auditoria — que TEM
// de ser exatamente [audit.KeyRefFor](subjectID) para o subject-binding de [audit.OpenContent].
func (v *vaultKeyVault) WrapDEK(subjectID string, dek []byte) ([]byte, string, error) {
	if subjectID == "" {
		return nil, "", audit.ErrNoSubject
	}
	ref := audit.KeyRefFor(subjectID)
	name := vaultKeyName(ref)
	if err := v.ensureTransitKey(name); err != nil {
		return nil, "", err
	}
	rb, code, err := v.do(http.MethodPost, "/v1/"+v.mount+"/encrypt/"+name,
		map[string]string{"plaintext": base64.StdEncoding.EncodeToString(dek)})
	if err != nil {
		return nil, "", fmt.Errorf("%w: encrypt: %v", ErrVaultKEK, err)
	}
	if code != http.StatusOK {
		return nil, "", fmt.Errorf("%w: encrypt: status %d", ErrVaultKEK, code)
	}
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.Data.Ciphertext == "" {
		return nil, "", fmt.Errorf("%w: resposta de encrypt sem ciphertext", ErrVaultKEK)
	}
	return []byte(out.Data.Ciphertext), ref, nil
}

// UnwrapDEK desembrulha a DEK DENTRO do Vault (decrypt). FAIL-CLOSED: chave destruída (crypto-shred)
// ou ciphertext adulterado ⇒ o Vault devolve erro ⇒ (nil,false); a DEK — logo o conteúdo — é
// irrecuperável. A KEK nunca entra no processo.
func (v *vaultKeyVault) UnwrapDEK(keyRef string, wrapped []byte) ([]byte, bool) {
	name := vaultKeyName(keyRef)
	rb, code, err := v.do(http.MethodPost, "/v1/"+v.mount+"/decrypt/"+name,
		map[string]string{"ciphertext": string(wrapped)})
	if err != nil || code != http.StatusOK {
		return nil, false // chave destruída/ausente ou embrulho inválido — irrecuperável
	}
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.Data.Plaintext == "" {
		return nil, false
	}
	dek, err := base64.StdEncoding.DecodeString(out.Data.Plaintext)
	if err != nil {
		return nil, false
	}
	return dek, true
}

// EnsureKey implementa [audit.KeyVault] honrando key-never-leaves: provisiona a chave Transit mas
// NUNCA devolve a KEK crua (key=nil). O caminho de escrita usa [WrapDEK], não isto.
func (v *vaultKeyVault) EnsureKey(subjectID string) ([]byte, string, error) {
	if subjectID == "" {
		return nil, "", audit.ErrNoSubject
	}
	ref := audit.KeyRefFor(subjectID)
	if err := v.ensureTransitKey(vaultKeyName(ref)); err != nil {
		return nil, "", err
	}
	return nil, ref, nil
}

// Key implementa [audit.KeyVault] honrando key-never-leaves: a KEK crua NUNCA é surrendida ⇒
// devolve sempre (nil,false). É esta recusa que força o caminho de envelope em [audit.OpenContent].
func (v *vaultKeyVault) Key(keyRef string) ([]byte, bool) { return nil, false }

// Delete implementa [audit.KeyVault] (crypto-shredding, GDPR Art. 17): destrói a chave Transit do
// titular no Vault ⇒ [UnwrapDEK] passa a falhar e o conteúdo fica irrecuperável. Idempotente
// (destruir o que não existe é no-op). A destruição exige deletion_allowed=true primeiro.
//
// LIMITAÇÃO da porta: [audit.KeyVault.Delete] não devolve erro — uma falha de rede aqui não pode
// ser sinalizada ao chamador (o mesmo vale para o vault in-memory). Em produção, o operador do
// pipeline DSAR deve VERIFICAR a destruição (ex.: reconciliação contra o Vault) antes de selar o
// desfecho como irrecuperável; a re-verificação de hash-chain do WORM não cobre a custódia externa.
// AOS-249 fecha a metade que a limitação da porta deixava aberta: a porta não deixa DEVOLVER o
// erro, mas nada obriga a IGNORÁ-LO. A destruição passa a ser VERIFICADA (a chave tem de deixar
// de existir) e uma destruição por confirmar põe a sonda de prontidão VERMELHA — é assim que um
// erase/expire falhado deixa de ser silencioso sem mudar a assinatura de [audit.KeyVault]. Com o
// token expirado, os três pedidos levam 403 e é exactamente este caminho que dispara.
func (v *vaultKeyVault) Delete(subjectID string) {
	name := vaultKeyName(audit.KeyRefFor(subjectID))
	// Habilita a destruição (config) e destrói. Best-effort idempotente; ignora 404 (já destruída).
	_, _, _ = v.do(http.MethodPost, "/v1/"+v.mount+"/keys/"+name+"/config", map[string]bool{"deletion_allowed": true})
	_, _, _ = v.do(http.MethodDelete, "/v1/"+v.mount+"/keys/"+name, nil)
	// VERIFICA em vez de confiar: no Vault, uma chave Transit destruída responde 404 à leitura.
	// Qualquer outra resposta (200 = ainda viva; 403 = sem autoridade para saber; erro de
	// transporte) NÃO prova a irrecuperabilidade que o titular vai ouvir dizer que aconteceu.
	_, code, err := v.doCtx(context.Background(), http.MethodGet, "/v1/"+v.mount+"/keys/"+name, nil)
	v.mu.Lock()
	defer v.mu.Unlock()
	if err == nil && code == http.StatusNotFound {
		delete(v.shredPend, name) // confirmada (limpa também uma tentativa anterior falhada)
		return
	}
	v.shredPend[name] = struct{}{} // `name` é o sha256 do keyRef — sem PII, seguro de expor
}

// Asserções de compile-time: o adaptador satisfaz ambas as portas (como o wrapper de referência).
var (
	_ audit.KeyVault   = (*vaultKeyVault)(nil)
	_ audit.KeyWrapper = (*vaultKeyVault)(nil)
)

// provaDeCapacidade responde à pergunta que a prontidão realmente faz — «esta credencial ainda
// consegue fazer o TRABALHO?» — em vez da pergunta que o `lookup-self` faz, que é «o Vault
// deixa-me consultar-me a mim próprio?».
//
// Lista as chaves do motor Transit: é a operação mais barata que a política do nó concede
// (`path "transit/keys" { capabilities = ["list"] }`), é READ-ONLY e não tem efeito nenhum.
//
// É evidência ESTRITAMENTE MAIS FORTE do que o `lookup-self` para a prontidão: testa a
// capacidade directamente em vez de a inferir da validade. O que se perde é o HORIZONTE — o TTL
// e a renovação — e essa perda é declarada, nunca escondida.
//
// LIMITE DECLARADO: se uma implantação também não conceder o `list`, esta prova falha e o nó
// declara o token morto sem o ser. Fica fail-closed (a direcção segura), e o remédio está escrito
// na mensagem: conceder `auth/token/lookup-self` à política.
func (v *vaultKeyVault) provaDeCapacidade(ctx context.Context) error {
	_, code, err := v.doCtx(ctx, "LIST", "/v1/"+v.mount+"/keys", nil)
	if err != nil {
		return fmt.Errorf("%w: prova de capacidade: %v", ErrVaultToken, err)
	}
	switch code {
	case http.StatusOK, http.StatusNotFound:
		// 404 é o Vault a dizer «autorizado, e não há chaves nenhumas» — um motor Transit
		// ainda vazio. Tratá-lo como falha faria o primeiro arranque de um nó novo nascer
		// vermelho, que é o oposto do que esta prova existe para evitar.
		return nil
	default:
		return fmt.Errorf("%w: prova de capacidade recusada (HTTP %d)", ErrVaultToken, code)
	}
}

// setTokenOpaco regista que a credencial FUNCIONA mas não se deixa medir.
//
// `tokenSeen` é actualizado porque houve, de facto, evidência fresca de que serve; `tokenTTL`
// fica a zero, que [tokenVerdict] já lê como «sem expiração conhecida». O flag próprio existe
// para o banner e as métricas NÃO confundirem este caso com um token periódico saudável: são
// posturas diferentes, e só uma delas tem uma data de morte que ninguém vê chegar.
func (v *vaultKeyVault) setTokenOpaco() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.tokenTTL, v.tokenSeen, v.tokenErr, v.tokenOpaco = 0, time.Now(), nil, true
}

// avisoDeOpacidade devolve o aviso a escrever no log — UMA vez, e não a cada tick.
//
// O comportamento anterior escrevia a mesma linha a cada minuto: 43 repetições em produção antes
// de alguém reparar. Um aviso repetido a essa cadência deixa de ser um aviso e passa a ser papel
// de parede, e foi exactamente isso que aconteceu.
func (v *vaultKeyVault) avisoDeOpacidade() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.tokenOpaco || v.opacoAvisado {
		return ""
	}
	v.opacoAvisado = true
	return "custodia da KEK (AOS-249): o token do Vault FUNCIONA (prova de capacidade contra " +
		v.mount + "/keys PASSOU) mas NAO SE DEIXA MEDIR — a politica nao lhe concede " +
		"`auth/token/lookup-self`, logo tambem nao concede `auth/token/renew-self`. CONSEQUENCIA: " +
		"o no NAO consegue renovar o token nem avisar antes de ele expirar. Um token periodico que " +
		"nunca e renovado MORRE no fim do periodo, e o /readyz so fica vermelho NESSE dia, sem " +
		"aviso previo. REMEDIO: acrescentar `path \"auth/token/lookup-self\" { capabilities = " +
		"[\"read\"] }` e `path \"auth/token/renew-self\" { capabilities = [\"update\"] }` a politica " +
		"do token (ver deploy/server/provision-identity.sh)"
}

// marcarShredPorConfirmar implementa [shredPendingMarker]: repõe, no ARRANQUE, uma pendência que
// a cadeia DSAR ainda afirma.
//
// PORQUE NÃO SE RE-SONDA O VAULT AQUI: a pergunta «esta chave existe?» é ambígua depois de um
// apagamento. Um titular apagado pode voltar a gerar dados, e o `EnsureKey` re-provisiona uma KEK
// NOVA — legitimamente. A cadeia é a única fonte que distingue «a destruição falhou» de «a
// destruição correu e o titular voltou». Ver [restoreShredPending].
//
// A pendência reposta limpa-se pelo caminho normal: um [vaultKeyVault.Delete] confirmado sobre a
// mesma chave apaga-a do conjunto.
func (v *vaultKeyVault) marcarShredPorConfirmar(subjectID string) {
	if subjectID == "" {
		return
	}
	name := vaultKeyName(audit.KeyRefFor(subjectID))
	v.mu.Lock()
	defer v.mu.Unlock()
	v.shredPend[name] = struct{}{}
}
