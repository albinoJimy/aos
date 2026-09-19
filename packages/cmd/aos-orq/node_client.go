package main

// Cliente do nó `aos` para o executor de nós do plano (AOS-413, ADR-027).
//
// Cada nó despachado de um plano é um run do nó `aos`: submete-se por `POST /runs` e acompanha-se
// por `GET /runs/{id}`. O nó continua a ser a única autoridade sobre o ciclo de vida desses runs
// (ADR-018); o `aos-orq` só os pede e lê o desfecho.
//
// DUAS credenciais, e não se confundem:
//   - o NHI do run, cunhado pelo OPERADOR com o `aos-issuer` e montado num ficheiro — vai no campo
//     `credential` do corpo: é em nome de quem o run age. Relê-se em cada submissão;
//   - o Bearer do IdP (`client_credentials`), com o segredo do cliente noutro ficheiro — vai no
//     `Authorization`: é quem CHAMA. Pede-se um NOVO em cada chamada, porque o nó aceita cada `jti`
//     uma só vez.
//
// Nenhum segredo entra por variável de ambiente: as variáveis só dizem ONDE estão os ficheiros.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Variáveis de ambiente do executor de nós (para as mensagens; o `os.Getenv` usa o literal,
// que é o que o gate da superfície de ambiente lê).
const (
	envNodeURL            = "AOS_ORQ_NODE_URL"
	envNodeCredentialFile = "AOS_ORQ_NODE_CREDENTIAL_FILE"
	envNodePrincipal      = "AOS_ORQ_NODE_PRINCIPAL"
	envOIDCTokenURL       = "AOS_ORQ_OIDC_TOKEN_URL"
	envOIDCClientID       = "AOS_ORQ_OIDC_CLIENT_ID"
	envOIDCSecretFile     = "AOS_ORQ_OIDC_CLIENT_SECRET_FILE"
)

// nodeClientTimeout limita cada pedido HTTP ao nó ou ao IdP.
const nodeClientTimeout = 30 * time.Second

// ErrNodeClientConfig — a configuração do executor de nós está incompleta ou incoerente.
var ErrNodeClientConfig = errors.New("aos-orq: executor de nos mal configurado")

// estadoDoRun é o desfecho de um run do nó, tal como o `GET /runs/{id}` o devolve.
type estadoDoRun struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Terminated bool   `json:"terminated,omitempty"`
	Error      string `json:"error,omitempty"`
	FinalText  string `json:"final_text,omitempty"`
}

// terminal diz se o run acabou: o nó marca `terminated` num run que concluiu nesta vida do
// processo, e devolve o desfecho durável (`failed`, `timed_out`, `killed`) de um que acabou antes.
func (e estadoDoRun) terminal() bool {
	if e.Terminated {
		return true
	}
	switch e.Status {
	case "completed", "failed", "timed_out", "killed":
		return true
	}
	return false
}

// concluiu diz se o run acabou COM SUCESSO: `completed`, `terminated` (o modelo deu a resposta
// final) e sem erro. O `completed` sozinho não chega: um run acabado em memória responde sempre
// `completed`, e um que parou por esgotar o orçamento ou os turnos vem com `terminated=false` —
// trabalho a meio, que não pode libertar os dependentes.
func (e estadoDoRun) concluiu() bool {
	return e.Status == "completed" && e.Terminated && e.Error == ""
}

// pedidoDeRun é o que se submete: o trabalho de UM nó do plano.
type pedidoDeRun struct {
	RunID     string
	Objective string
	// Tools é a lista-branca do run: as tools pinadas do nó no `plan.materialized`.
	Tools []string
}

// nodeClient fala com a API do nó.
type nodeClient struct {
	base      string
	http      *http.Client
	credFile  string
	principal string
	// bearer devolve um token NOVO do IdP para cada chamada; nil ⇒ sem `Authorization` (nó sem
	// gate soberano, fora de produção).
	bearer func(ctx context.Context) (string, error)
}

// nodeClientDoAmbiente compõe o cliente a partir do ambiente. Sem AOS_ORQ_NODE_URL devolve
// (nil, nil): o executor não está composto e o `serve` despacha sem executar, como antes.
func nodeClientDoAmbiente() (*nodeClient, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AOS_ORQ_NODE_URL")), "/")
	if base == "" {
		return nil, nil
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: %s=%q não é um URL http(s)", ErrNodeClientConfig, envNodeURL, base)
	}
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("AOS_MODE")), "production")
	if production && u.Scheme != "https" && !hostInterno(u.Hostname()) {
		return nil, fmt.Errorf("%w: em produção o nó fala-se por https, ou por http só dentro da rede do compose (nome de serviço ou loopback) — %s=%q", ErrNodeClientConfig, envNodeURL, base)
	}
	credFile := strings.TrimSpace(os.Getenv("AOS_ORQ_NODE_CREDENTIAL_FILE"))
	if credFile == "" {
		return nil, fmt.Errorf("%w: %s definido sem %s — o NHI do run, cunhado pelo operador, é obrigatório", ErrNodeClientConfig, envNodeURL, envNodeCredentialFile)
	}
	c := &nodeClient{
		base: base,
		// SEM redirects: num 307/308 o Go reenvia o CORPO — com o NHI do run, ou o segredo do
		// cliente no pedido de token — para o destino novo, noutro host ou em claro. Um redirect
		// aqui é uma resposta inesperada, e trata-se como erro.
		http: &http.Client{
			Timeout: nodeClientTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		credFile:  credFile,
		principal: strings.TrimSpace(os.Getenv("AOS_ORQ_NODE_PRINCIPAL")),
	}
	if c.principal == "" {
		c.principal = "agent:aos-orq"
	}

	tokenURL := strings.TrimSpace(os.Getenv("AOS_ORQ_OIDC_TOKEN_URL"))
	clientID := strings.TrimSpace(os.Getenv("AOS_ORQ_OIDC_CLIENT_ID"))
	secretFile := strings.TrimSpace(os.Getenv("AOS_ORQ_OIDC_CLIENT_SECRET_FILE"))
	switch {
	case tokenURL == "" && clientID == "" && secretFile == "":
		if production {
			return nil, fmt.Errorf("%w: em produção o nó exige um Bearer do IdP — defina %s, %s e %s", ErrNodeClientConfig, envOIDCTokenURL, envOIDCClientID, envOIDCSecretFile)
		}
	case tokenURL == "" || clientID == "" || secretFile == "":
		return nil, fmt.Errorf("%w: o Bearer do IdP exige os três — %s, %s e %s", ErrNodeClientConfig, envOIDCTokenURL, envOIDCClientID, envOIDCSecretFile)
	default:
		tu, err := url.Parse(tokenURL)
		if err != nil || (tu.Scheme != "http" && tu.Scheme != "https") || tu.Host == "" {
			return nil, fmt.Errorf("%w: %s=%q não é um URL http(s)", ErrNodeClientConfig, envOIDCTokenURL, tokenURL)
		}
		// O pedido de token leva o SEGREDO do cliente no corpo: em produção só por https, sem a
		// excepção da rede interna (o IdP serve https mesmo lá dentro).
		if production && tu.Scheme != "https" {
			return nil, fmt.Errorf("%w: em produção o token do IdP pede-se por https (%s=%q)", ErrNodeClientConfig, envOIDCTokenURL, tokenURL)
		}
		c.bearer = clientCredentials(c.http, tokenURL, clientID, secretFile)
	}
	return c, nil
}

// hostInterno diz se o host é o de um serviço da rede do compose (um nome sem pontos, como `aos`)
// ou loopback. É o troço que o edge já percorre em claro até ao nó: o TLS termina no edge
// (AOS_TLS_EXTERNAL_TERMINATION) e o nó escuta HTTP em `aos:8080`. Exigir https aqui seria mais
// estrito do que a arquitectura em vigor e deixava o executor sem caminho até ao nó; um host
// com pontos (um nome público, um IP) continua a exigir https.
func hostInterno(host string) bool {
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	return host != "" && !strings.ContainsAny(host, ".:")
}

// clientCredentials devolve a função que pede um token NOVO ao IdP em cada chamada. O segredo
// relê-se do ficheiro de cada vez (rodá-lo não exige reiniciar).
func clientCredentials(hc *http.Client, tokenURL, clientID, secretFile string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		secret, err := lerSegredo(secretFile)
		if err != nil {
			return "", err
		}
		form := url.Values{}
		form.Set("grant_type", "client_credentials")
		form.Set("client_id", clientID)
		form.Set("client_secret", secret)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := hc.Do(req)
		if err != nil {
			return "", fmt.Errorf("token do IdP: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("token do IdP: HTTP %d", resp.StatusCode)
		}
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
			return "", errors.New("token do IdP: resposta sem access_token")
		}
		return tok.AccessToken, nil
	}
}

// lerSegredo lê um ficheiro de segredo e tira o fim de linha.
func lerSegredo(caminho string) (string, error) {
	b, err := os.ReadFile(caminho)
	if err != nil {
		return "", fmt.Errorf("ler %s: %w", caminho, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("%s está vazio", caminho)
	}
	return s, nil
}

// autenticar põe o Bearer no pedido, se o cliente o tiver.
func (c *nodeClient) autenticar(ctx context.Context, req *http.Request) error {
	if c.bearer == nil {
		return nil
	}
	tok, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// errRunFilhoJaExiste — o nó já tinha um run com o id do run filho (409). Numa PRIMEIRA
// submissão isso não é idempotência: é um run que este plano não criou — com outro objectivo e
// outras tools — e aceitar o seu desfecho era aceitar um veredicto alheio.
var errRunFilhoJaExiste = errors.New("aos-orq: o no ja tem um run com o id do run filho, e nao foi este plano que o criou")

// Submit pede ao nó que hospede o run do nó do plano. Um 409 devolve [errRunFilhoJaExiste]: o
// executor só submete um nó que estava pendente, pelo que o run não pode ser seu. (Num nó SEM
// gate soberano a colisão responde 201 e não é detectável — fora de produção.)
func (c *nodeClient) Submit(ctx context.Context, p pedidoDeRun) error {
	cred, err := lerSegredo(c.credFile)
	if err != nil {
		return fmt.Errorf("NHI do run: %w", err)
	}
	corpo, err := json.Marshal(map[string]any{
		"run_id":        p.RunID,
		"objective":     p.Objective,
		"principal_nhi": c.principal,
		"credential":    cred,
		"tools":         p.Tools,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/runs", bytes.NewReader(corpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.autenticar(ctx, req); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("submeter %s ao nó: %w", p.RunID, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", errRunFilhoJaExiste, p.RunID)
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("submeter %s ao nó: HTTP %d %s", p.RunID, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
}

// Status lê o estado do run. (_, false, nil) ⇒ o nó não o conhece (404): nunca submetido, ou um
// run que morreu antes de ter estado durável.
func (c *nodeClient) Status(ctx context.Context, runID string) (estadoDoRun, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		return estadoDoRun{}, false, err
	}
	if err := c.autenticar(ctx, req); err != nil {
		return estadoDoRun{}, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return estadoDoRun{}, false, fmt.Errorf("estado de %s no nó: %w", runID, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var e estadoDoRun
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&e); err != nil {
			return estadoDoRun{}, false, fmt.Errorf("estado de %s no nó: resposta ilegível: %w", runID, err)
		}
		return e, true, nil
	case http.StatusNotFound:
		return estadoDoRun{}, false, nil
	default:
		return estadoDoRun{}, false, fmt.Errorf("estado de %s no nó: HTTP %d", runID, resp.StatusCode)
	}
}
