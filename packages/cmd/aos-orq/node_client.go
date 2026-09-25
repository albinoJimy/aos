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
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
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
	// Inputs são os payloads que o `consumes` DESTE nó declara (AOS-414). Vão ao tail do run
	// como segmentos untrusted, com a proveniência do contrato.
	Inputs []entradaDoNo
}

// entradaDoNo é um payload entregue ao run de um nó: o contrato que o declara, o digest do
// conteúdo e o conteúdo.
type entradaDoNo struct {
	From    string `json:"from"`
	Output  string `json:"output"`
	Digest  string `json:"digest"`
	Content string `json:"content"`
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
		return nil, fmt.Errorf("%w: %s=%q não é um URL http(s)", ErrNodeClientConfig, "AOS_ORQ_NODE_URL", base)
	}
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("AOS_MODE")), "production")
	if production && u.Scheme != "https" && !hostInterno(u.Hostname()) {
		return nil, fmt.Errorf("%w: em produção o nó fala-se por https, ou por http só dentro da rede do compose (nome de serviço ou loopback) — %s=%q", ErrNodeClientConfig, "AOS_ORQ_NODE_URL", base)
	}
	credFile := strings.TrimSpace(os.Getenv("AOS_ORQ_NODE_CREDENTIAL_FILE"))
	if credFile == "" {
		return nil, fmt.Errorf("%w: %s definido sem %s — o NHI do run, cunhado pelo operador, é obrigatório", ErrNodeClientConfig, "AOS_ORQ_NODE_URL", "AOS_ORQ_NODE_CREDENTIAL_FILE")
	}
	// AOS-416: o NHI é a OUTRA credencial montada, com o mesmo uid e o mesmo sintoma. O operador
	// copia-o à mão para `secrets/`/`orq/` com `umask 077`, o que dá 0600 do utilizador dele —
	// ilegível pelo contentor. Corrigir só o segredo do IdP fechava uma porta e deixava a outra
	// aberta na mesma parede.
	if err := validarCredencialDeFicheiro("AOS_ORQ_NODE_CREDENTIAL_FILE", credFile); err != nil {
		return nil, err
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
			return nil, fmt.Errorf("%w: em produção o nó exige um Bearer do IdP — defina %s, %s e %s", ErrNodeClientConfig, "AOS_ORQ_OIDC_TOKEN_URL", "AOS_ORQ_OIDC_CLIENT_ID", "AOS_ORQ_OIDC_CLIENT_SECRET_FILE")
		}
	case tokenURL == "" || clientID == "" || secretFile == "":
		return nil, fmt.Errorf("%w: o Bearer do IdP exige os três — %s, %s e %s", ErrNodeClientConfig, "AOS_ORQ_OIDC_TOKEN_URL", "AOS_ORQ_OIDC_CLIENT_ID", "AOS_ORQ_OIDC_CLIENT_SECRET_FILE")
	default:
		tu, err := url.Parse(tokenURL)
		if err != nil || (tu.Scheme != "http" && tu.Scheme != "https") || tu.Host == "" {
			return nil, fmt.Errorf("%w: %s=%q não é um URL http(s)", ErrNodeClientConfig, "AOS_ORQ_OIDC_TOKEN_URL", tokenURL)
		}
		// O pedido de token leva o SEGREDO do cliente no corpo: em produção só por https, sem a
		// excepção da rede interna (o IdP serve https mesmo lá dentro).
		if production && tu.Scheme != "https" {
			return nil, fmt.Errorf("%w: em produção o token do IdP pede-se por https (%s=%q)", ErrNodeClientConfig, "AOS_ORQ_OIDC_TOKEN_URL", tokenURL)
		}
		// AOS-416 — A CREDENCIAL VERIFICA-SE AQUI, NÃO NA PRIMEIRA SUBMISSÃO.
		//
		// Até aqui o arranque só via que a string do caminho não estava vazia. Com o ficheiro
		// ilegível — que era o estado REAL em produção, `0400` do utilizador `aos` contra um
		// contentor que corre como 65532 — a composição passava, o banner dizia COMPOSTO, e a
		// falha só aparecia na primeira submissão de nó. Isso é o modo de falha do AOS-413 a
		// regressar por outra porta: o plano despacha e nada executa.
		if err := validarCredencialDeFicheiro("AOS_ORQ_OIDC_CLIENT_SECRET_FILE", secretFile); err != nil {
			return nil, err
		}
		c.bearer = clientCredentials(c.http, tokenURL, clientID, secretFile)
	}
	return c, nil
}

// uidDoContentor é o uid não-root da imagem (`USER 65532:65532`, deploy/node/Dockerfile). Está
// aqui como número porque é isso que aparece nos `ls -l` do host: o host não tem utilizador com
// este nome, e a mensagem de erro tem de ser reconhecível por quem olha para o ficheiro.
const uidDoContentor = 65532

// validarCredencialDeFicheiro prova, no ARRANQUE, que uma credencial montada em ficheiro existe,
// é LEGÍVEL por este processo e não está vazia. Serve as duas do executor: o NHI do run e o
// segredo do cliente do IdP.
//
// # PORQUE É QUE A VALIDAÇÃO É UMA LEITURA, E SÓ UMA LEITURA
//
// Um `os.Stat` diz que o ficheiro existe; não diz que este processo o consegue LER. A diferença é
// exactamente o defeito que o AOS-416 corrige — `0400` do utilizador `aos` contra um contentor que
// corre como 65532 — e um `Stat` teria passado por cima dele.
//
// E como o `Stat` nada acrescenta a uma leitura que já distingue ausente de ilegível de vazio, não
// está aqui: seria uma segunda ocorrência de G703 na baseline do gosec a troco de nada.
//
// # PORQUE É QUE O MODO NÃO É POLÍTICA AQUI
//
// A primeira versão deste código recusava em produção qualquer ficheiro com bits de grupo ou de
// outros, por entender que `0644` punha o segredo «ao alcance de qualquer processo da máquina».
// Isso é falso neste deployment e a revisão adversarial mostrou-o: `deploy/server/bootstrap.sh`
// cria `secrets/` com `install -d -m 700` e o `provision.sh` reforça-o — medido em produção,
// `drwx------ aos aos`. **O directório é a fronteira**; sem travessia, o modo do ficheiro lá
// dentro não abre nada a ninguém. A regra teria recusado a configuração CORRECTA (a convenção
// `0644` que todos os outros segredos montados seguem) e empurrado para um `chown` que parte o
// backup nocturno — que corre como `aos` e tara o `secrets/` inteiro.
//
// O que fica é a propriedade que importa e que se pode provar aqui: o processo consegue ler.
func validarCredencialDeFicheiro(variavel, caminho string) error {
	_, err := lerSegredo(caminho)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %s=%q está configurado mas o ficheiro NÃO existe — sem ele o executor não fala com o nó",
			ErrNodeClientConfig, variavel, caminho)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: %s=%q: %s", ErrNodeClientConfig, variavel, caminho, comoAbrirAoContentor(caminho))
	default:
		return fmt.Errorf("%w: %s=%q: %v", ErrNodeClientConfig, variavel, caminho, err)
	}
}

// comoAbrirAoContentor é a metade accionável da mensagem: o operador tem de saber o gesto, senão
// inventa um. Na validação do AOS-415 a adivinha produziu uma CÓPIA do segredo em 0444 — e é
// dessa cópia que este ticket nasceu.
func comoAbrirAoContentor(caminho string) string {
	return fmt.Sprintf("o ficheiro existe mas este processo NÃO o consegue ler. O contentor corre como uid %d: no host, `chmod 0644 %s` — é a convenção dos outros segredos montados (model-api.key, vault-token), e o directório `secrets/` em 0700 continua a ser a fronteira. NÃO faça uma cópia do ficheiro",
		uidDoContentor, caminho)
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

// lerSegredo lê um ficheiro de segredo e tira o fim de linha. O erro do SO viaja embrulhado com
// `%w` de propósito: quem chama distingue `fs.ErrPermission` de `fs.ErrNotExist`, que são dois
// problemas de operação diferentes (AOS-416).
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
		"inputs":        p.Inputs,
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

// toolDoNo é UMA tool do catálogo do nó (`GET /tools`, AOS-441): o nome que a lista-branca
// compara, a versão e o digest do contrato (um pin do contrato — schema, scopes, egress — pela
// fórmula do registo do nó, não prova de que algo foi assinado), e os dois eixos de risco que o
// manifesto do nó declara, já normalizados fail-closed por ele (`egress` não declarado ⇒
// `unknown`; `reversibility` que não seja «reversible» ⇒ `irreversible`).
type toolDoNo struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Digest        string `json:"digest"`
	Egress        string `json:"egress"`
	Reversibility string `json:"reversibility"`
}

// CatalogoDeTools lê o catálogo de tools do nó — as tools que ele oferece ao modelo.
//
// Tudo o que não seja um 200 com `tools` presente é ERRO, incluindo o 404 de um nó anterior ao
// AOS-441: sem o catálogo não há com que comparar o snapshot, e o lado seguro é não arrancar.
func (c *nodeClient) CatalogoDeTools(ctx context.Context) ([]toolDoNo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/tools", nil)
	if err != nil {
		return nil, err
	}
	if err := c.autenticar(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catálogo de tools do nó: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.New("catálogo de tools do nó: GET /tools deu 404 — o nó é anterior ao AOS-441 e não expõe o catálogo; sem ele o snapshot não se confere")
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("catálogo de tools do nó: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var corpo struct {
		Tools *[]toolDoNo `json:"tools"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&corpo); err != nil {
		return nil, fmt.Errorf("catálogo de tools do nó: resposta ilegível: %w", err)
	}
	if corpo.Tools == nil {
		return nil, errors.New("catálogo de tools do nó: resposta sem `tools`")
	}
	for i, t := range *corpo.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return nil, fmt.Errorf("catálogo de tools do nó: tool #%d sem nome", i)
		}
	}
	return *corpo.Tools, nil
}

// pedidoReclamado é um pedido de plano que este consumidor tomou para si.
//
// A `Geracao` viaja porque é ela que amarra o DESFECHO à tentativa: sem ela, um desfecho
// reportado tarde podia fechar uma tentativa que já não é a corrente, e um pedido que voltou à
// fila por expiração ficaria terminado por um relatório de uma corrida anterior.
type pedidoReclamado struct {
	RunID     string `json:"run_id"`
	Objective string `json:"objective"`
	Board     string `json:"board"`
	Region    string `json:"region"`
	Geracao   int    `json:"generation"`
}

// ReclamarPedido pede ao nó UM pedido de plano pendente, reclamando-o.
//
// `(_, false, nil)` ⇒ não há nada para este consumidor (204). O 204 é o mesmo quer a fila esteja
// vazia, quer tudo o que lá está pertença a outra região — é a postura de não-oracularidade do
// ADR-030 §2.1, e este cliente não tenta distinguir os dois casos porque o nó não lhos diz.
//
// NÃO RETENTA, como o resto deste cliente. Um erro aqui aborta a drenagem, e a invocação seguinte
// (timer ou laço) recomeça — a fila é durável e nada se perde por desistir cedo.
func (c *nodeClient) ReclamarPedido(ctx context.Context) (pedidoReclamado, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/plans/claim", nil)
	if err != nil {
		return pedidoReclamado{}, false, err
	}
	if err := c.autenticar(ctx, req); err != nil {
		return pedidoReclamado{}, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return pedidoReclamado{}, false, fmt.Errorf("reclamar pedido no nó: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var p pedidoReclamado
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p); err != nil {
			return pedidoReclamado{}, false, fmt.Errorf("reclamar pedido no nó: resposta ilegível: %w", err)
		}
		if p.RunID == "" || p.Geracao < 1 {
			// Um pedido sem id ou sem geração não é reclamável: reportar o desfecho seria
			// impossível e o pedido ficaria preso até ao TTL. Melhor falhar alto.
			return pedidoReclamado{}, false, fmt.Errorf("reclamar pedido no nó: resposta sem run_id/generation")
		}
		return p, true, nil
	case http.StatusNoContent:
		return pedidoReclamado{}, false, nil
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return pedidoReclamado{}, false, fmt.Errorf("reclamar pedido no nó: HTTP %d %s",
			resp.StatusCode, strings.TrimSpace(string(msg)))
	}
}

// ReportarDesfecho diz ao nó como correu UMA tentativa.
//
// É o que distingue «volta à fila já» de «não volta nunca». Sem isto o pedido fica preso até a
// reclamação expirar — meia hora de silêncio por uma falha conhecida no primeiro segundo.
func (c *nodeClient) ReportarDesfecho(ctx context.Context, runID string, geracao int, classe string, codigo int, detalhe string) error {
	corpo, err := json.Marshal(map[string]any{
		"run_id":       runID,
		"generation":   geracao,
		"classe":       classe,
		"codigo_saida": codigo,
		"detalhe":      detalhe,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/plans/outcome", bytes.NewReader(corpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.autenticar(ctx, req); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reportar desfecho de %s: %w", runID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("reportar desfecho de %s: HTTP %d %s", runID, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
