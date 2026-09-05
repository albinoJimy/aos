package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ADAPTADOR REMOTO DA PORTA DE ATTESTATION (remediação AOS-177 — "enforcement dormente").
//
// A verificação WebAuthn precisa de um descodificador CBOR, e a Carta (emenda 1.3) confina
// essa dependência ao COMPONENTE DE AUTORIDADE EXTERNO: o binário do nó nunca pode importar
// packages/platform/attestation (o guarda de packages/cmd/aos/dep_isolation_test.go proíbe-o
// executavelmente). A consequência, antes desta remediação, era que NENHUM caminho de produção
// conseguia activar a verificação: a porta existia, a implementação existia, e não havia como
// ligá-las — o 4-eyes do nó ficava estrutural em todos os artefactos que shipam.
//
// Este ficheiro fecha esse buraco SEM violar o isolamento: um cliente HTTP de STDLIB PURA
// (net/http + encoding/json + encoding/base64) que fala com o componente de autoridade
// externo, que por sua vez corre o [attestation.Verifier] com a dependência CBOR. A porta é
// bytes-in/bytes-out, pelo que o wire é trivial e NENHUM tipo de CBOR/WebAuthn atravessa a
// fronteira do nó.
//
// FAIL-CLOSED EM TODOS OS EIXOS: destino não-HTTPS fora de loopback recusado na construção;
// timeout obrigatório; qualquer status != 200, corpo malformado, corpo acima do tecto, campo
// `device_id` ausente/vazio ou base64 inválido ⇒ ERRO (nunca um deviceID utilizável). Um
// componente de autoridade indisponível NEGA a aprovação — não a degrada para o modo
// estrutural.

// Erros do adaptador remoto (todos fail-closed).
var (
	// ErrRemoteAttestationURL — URL vazia, malformada, com esquema não-HTTP(S), ou HTTP em
	// claro para um destino que não é loopback. Uma attestation trocada em claro pela rede é
	// manipulável em trânsito: o veredicto do verificador (um "sim") viajaria sem protecção.
	ErrRemoteAttestationURL = errors.New("integration: attestation remota: URL inválida (exige https, ou http só em loopback)")
	// ErrRemoteAttestationUnavailable — falha de transporte/timeout ao falar com o componente
	// de autoridade. NEGA (nunca degrada para o modo estrutural).
	ErrRemoteAttestationUnavailable = errors.New("integration: attestation remota: componente de autoridade indisponível (fail-closed)")
	// ErrRemoteAttestationStatus — o componente respondeu com status != 200 (inclui a recusa
	// legítima da attestation, que o gate converte em ErrDeviceAttestationRejected).
	ErrRemoteAttestationStatus = errors.New("integration: attestation remota: resposta não-OK do componente de autoridade")
	// ErrRemoteAttestationBody — resposta ilegível, acima do tecto, ou sem um device_id
	// utilizável.
	ErrRemoteAttestationBody = errors.New("integration: attestation remota: resposta malformada (fail-closed)")
)

// Tectos do adaptador remoto: uma resposta legítima tem umas dezenas de bytes (um digest em
// base64); o pedido leva a attestation, que é de poucos KiB.
const (
	// maxRemoteAttestationResponseBytes — tecto do corpo da resposta lida.
	maxRemoteAttestationResponseBytes = 8 << 10
	// defaultRemoteAttestationTimeout — timeout por-verificação quando não é configurado.
	defaultRemoteAttestationTimeout = 5 * time.Second
)

// RemoteAttestationConfig configura o adaptador. Só material NÃO-SECRETO tem defaults: o token
// (se existir) vem do chamador, que o carrega de config/vault.
type RemoteAttestationConfig struct {
	// URL é o endpoint de verificação do componente de autoridade externo (POST JSON).
	// Exige https, salvo loopback explícito (127.0.0.1/::1/localhost) para desenvolvimento.
	URL string
	// Timeout é o tecto por-verificação. <=0 ⇒ [defaultRemoteAttestationTimeout]. Um gate sem
	// timeout ficaria pendurado num componente lento — e um humano à espera acaba por
	// contornar o controlo.
	Timeout time.Duration
	// AuthToken, quando não-vazio, é enviado como `Authorization: Bearer <token>`. NUNCA é
	// registado em erros (os erros deste ficheiro não incluem cabeçalhos nem corpo do pedido).
	AuthToken string
	// Client permite injectar um *http.Client (testes, mTLS, proxies da organização). nil ⇒
	// cliente próprio com o Timeout acima.
	Client *http.Client
}

// RemoteDeviceAttestationVerifier implementa [DeviceAttestationVerifier] delegando no
// componente de autoridade externo. Seguro para uso concorrente (o *http.Client é-o e o resto
// é imutável).
type RemoteDeviceAttestationVerifier struct {
	url     string
	token   string
	client  *http.Client
	timeout time.Duration
}

// remoteAttestationRequest/remoteAttestationResponse são o wire (base64 std, como o resto da
// API do nó). NENHUM tipo de CBOR aparece aqui — é isso que mantém o nó zero-dep.
type remoteAttestationRequest struct {
	AttestationObject string `json:"attestation_object"`
	ClientDataJSON    string `json:"client_data_json"`
	ExpectedChallenge string `json:"expected_challenge"`
}

type remoteAttestationResponse struct {
	DeviceID string `json:"device_id"`
	// Error é o motivo da recusa, quando o componente o quiser dar. É opaco para o nó e só
	// entra no erro devolvido depois de SANEADO (comprimento e caracteres de controlo).
	Error string `json:"error,omitempty"`
}

// NewRemoteDeviceAttestationVerifier valida a configuração e constrói o adaptador. Fail-closed
// na construção: uma URL inaceitável não produz um verificador "que às vezes funciona".
func NewRemoteDeviceAttestationVerifier(cfg RemoteAttestationConfig) (*RemoteDeviceAttestationVerifier, error) {
	if err := checkRemoteAttestationURL(cfg.URL); err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRemoteAttestationTimeout
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &RemoteDeviceAttestationVerifier{
		url:     cfg.URL,
		token:   cfg.AuthToken,
		client:  client,
		timeout: timeout,
	}, nil
}

// checkRemoteAttestationURL exige https, ou http APENAS para loopback (o mesmo critério de
// bind não-loopback do resto do nó: em claro só onde não há rede a atravessar). O critério
// vive em [CheckSecureTransportURL] — este invólucro só lhe cola a sentinela do adaptador.
func checkRemoteAttestationURL(raw string) error {
	if err := CheckSecureTransportURL(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrRemoteAttestationURL, err)
	}
	return nil
}

// CheckSecureTransportURL é o CRITÉRIO DE TRANSPORTE das URLs de saída do nó: exige https, ou
// http APENAS para loopback (127.0.0.0/8, ::1, "localhost") — em claro só onde não há rede a
// atravessar, o mesmo princípio do bind-guardrail do ingresso.
//
// Está EXPORTADO (e o critério deixou de viver dentro de [checkRemoteAttestationURL]) porque o
// binário do nó tem mais do que um destino de saída que carrega uma credencial no pedido — a
// attestation remota e a custódia da KEK no Vault (AOS-249/F6). Ter DOIS critérios escritos à
// mão era exactamente o defeito: um deles acabaria mais fraco que o outro, e a divergência só
// se notaria com o token já na rede em claro.
//
// Devolve nil, ou um erro DESCRITIVO e SEM sentinela — cada chamador envolve-o na SUA sentinela
// nomeada, que é o que o operador vê no aborto do arranque.
//
// USER-INFO É RECUSADO, e a versão anterior deste comentário afirmava que não era preciso:
// «a mensagem nunca inclui credenciais (user-info numa URL não é suportado por nenhum chamador)».
// Era falso nas duas metades (AOS-333). Nada rejeitava `https://user:pass@vault:8200`, e o ramo
// de parse falhado devolvia `fmt.Errorf("%q", raw)` — que o `%v` do wrap de cada chamador ecoava
// inteiro, credenciais incluídas, para o log de arranque.
//
// Uma senha de Vault num log recolhido, agregado e retido é o mesmo segredo em claro que o
// ADR-006 proíbe; muda só quem o lê — aqui não é o agente, é quem quer que leia o colector. E é
// forma legítima de passar credenciais a um Vault, pelo que não bastava contar com ninguém a
// usá-la: um critério que depende de o operador não fazer a coisa documentada não é critério.
//
// A MENSAGEM DE ERRO NUNCA ECOA A URL. Onde o valor ajudaria o diagnóstico usa-se
// [RedactURL], que preserva esquema, host e porta e deita fora o resto — e no ramo de parse
// falhado não se ecoa nada, porque uma URL que o parser recusou não se sabe redigir.
func CheckSecureTransportURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("vazia")
	}
	// TrimSpace ANTES do Parse, e não só no teste de vazio: uma variável de ambiente com
	// um espaço à direita (`.env`, compose, Helm) é uma URL perfeitamente válida com um
	// erro de transcrição, e sem isto abortava o arranque com «malformada (valor omitido)»
	// — uma mensagem que, por não poder ecoar o valor, não deixava ver que o problema era
	// um espaço. [RedactURL] já apara; as duas funções têm de concordar sobre o que é uma
	// URL, senão a mesma entrada é inválida numa e publicável na outra.
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		// NÃO se ecoa `raw`: uma URL que o parser recusou não se sabe redigir com
		// segurança, e é precisamente numa URL malformada que uma credencial mal
		// escapada tem mais probabilidade de estar.
		return errors.New("malformada (valor omitido)")
	}
	if u.User != nil {
		// Fail-closed ANTES do esquema: uma URL com credenciais é recusada mesmo
		// quando o transporte estaria correcto. O nome de utilizador NÃO é ecoado —
		// numa URL de Vault ele identifica o principal, e a senha vem colada a ele.
		//
		// ISTO É UMA QUEBRA DELIBERADA, e não a remoção de uma forma que ninguém usava.
		// O `net/http` converte `req.URL.User` em `Authorization: Basic`, pelo que um
		// verificador de attestation atrás de um proxy com basic-auth funcionava assim —
		// medido na revisão adversarial deste ticket. A recusa mantém-se porque o eixo é
		// o segredo, não o transporte: uma credencial num URL de ambiente aparece na
		// tabela de processos, no `inspect` do contentor e em qualquer mensagem de erro
		// que ecoe o endereço, e nenhum desses sítios se fecha caso a caso.
		//
		// A MENSAGEM NOMEIA OS DOIS REMÉDIOS porque eles não são o mesmo: o ficheiro de
		// token dá `Bearer` (Vault e attestation), e para quem precisava de `Basic` a via
		// é terminar a autenticação no proxy. Prescrever só o token seria mandar o
		// operador por um caminho que não repõe o deployment dele.
		return errors.New("traz credenciais no URL (user-info); use o ficheiro de token (Authorization: Bearer) ou termine a autenticacao no proxy — a basic-auth embutida no URL deixou de ser aceite (AOS-333)")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("http em claro para %q (só loopback)", host)
	default:
		return fmt.Errorf("esquema %q", u.Scheme)
	}
}

// RedactURL devolve a forma PUBLICÁVEL de uma URL: esquema, host e porta, e mais nada.
//
// Existe porque um banner de postura tem de dizer ONDE o nó fala sem dizer COM QUE
// credencial (AOS-333). O `brokerVaultPostureBanner` imprimia `AOS_BROKER_VAULT_ADDR`
// cru, pelo que uma senha embutida ia em texto claro para o log de arranque.
//
// Descarta user-info, caminho, query e fragmento — tudo o que possa carregar segredo —
// e devolve `(inválida)` para o que não se souber analisar, nunca o valor original. É
// deliberadamente mais estreita do que o necessário: preservar o caminho ajudaria o
// diagnóstico, mas um caminho de Vault já carregou tokens em incidentes reais noutros
// sistemas, e um redactor que hesita não serve para o sítio onde é preciso.
func RedactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(inválida)"
	}
	// Compõe a partir dos DOIS campos que se querem preservar, em vez de concatenar à
	// mão: uma entrada sem esquema (`//vault:8200`) devolvia `vault:8200`, uma forma que
	// o doc-comment acima não promete e que num banner se lê como um host solto. Aqui o
	// resultado é sempre re-analisável como a mesma coisa que entrou.
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// VerifyDeviceAttestation satisfaz [DeviceAttestationVerifier]. Delega a verificação
// criptográfica no componente externo e devolve o identificador OPACO de dispositivo.
func (r *RemoteDeviceAttestationVerifier) VerifyDeviceAttestation(ctx context.Context, attestationObject, clientDataJSON, expectedChallenge []byte) ([]byte, error) {
	if len(attestationObject) == 0 || len(clientDataJSON) == 0 || len(expectedChallenge) == 0 {
		return nil, fmt.Errorf("%w: entrada incompleta", ErrRemoteAttestationBody)
	}
	body, err := json.Marshal(remoteAttestationRequest{
		AttestationObject: base64.StdEncoding.EncodeToString(attestationObject),
		ClientDataJSON:    base64.StdEncoding.EncodeToString(clientDataJSON),
		ExpectedChallenge: base64.StdEncoding.EncodeToString(expectedChallenge),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: codificar pedido: %v", ErrRemoteAttestationBody, err)
	}

	// Timeout PRÓPRIO por-verificação, além do ctx do chamador: o gate nunca fica pendurado.
	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRemoteAttestationUnavailable, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		// O erro de transporte pode conter a URL, que não é segredo; o token NUNCA entra aqui
		// (vai em cabeçalho e o net/http não o inclui no erro).
		return nil, fmt.Errorf("%w: %v", ErrRemoteAttestationUnavailable, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRemoteAttestationResponseBytes))
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteAttestationResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: ler resposta: %v", ErrRemoteAttestationBody, err)
	}
	if len(raw) > maxRemoteAttestationResponseBytes {
		return nil, fmt.Errorf("%w: resposta acima do tecto (%d bytes)", ErrRemoteAttestationBody, maxRemoteAttestationResponseBytes)
	}

	var out remoteAttestationResponse
	decodeErr := json.Unmarshal(raw, &out)

	if resp.StatusCode != http.StatusOK {
		// Recusa do componente (403 com motivo) ou avaria (5xx): em ambos os casos NEGA. O
		// motivo é saneado antes de entrar no erro (entrada remota não dita o que vai a logs).
		reason := ""
		if decodeErr == nil {
			reason = sanitizeRemoteReason(out.Error)
		}
		if reason == "" {
			return nil, fmt.Errorf("%w: status %d", ErrRemoteAttestationStatus, resp.StatusCode)
		}
		return nil, fmt.Errorf("%w: status %d: %s", ErrRemoteAttestationStatus, resp.StatusCode, reason)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("%w: JSON inválido: %v", ErrRemoteAttestationBody, decodeErr)
	}
	if out.DeviceID == "" {
		return nil, fmt.Errorf("%w: sem device_id", ErrRemoteAttestationBody)
	}
	deviceID, err := base64.StdEncoding.DecodeString(out.DeviceID)
	if err != nil || len(deviceID) == 0 {
		return nil, fmt.Errorf("%w: device_id não é base64 utilizável", ErrRemoteAttestationBody)
	}
	return deviceID, nil
}

// sanitizeRemoteReason limita o motivo devolvido pelo componente remoto: comprimento fixo e
// sem caracteres de controlo (nem sequer o componente de autoridade dita o que entra nos logs
// do nó).
func sanitizeRemoteReason(s string) string {
	const max = 96
	out := make([]rune, 0, max)
	for _, r := range s {
		if len(out) == max {
			break
		}
		if r < 0x20 || r == 0x7f {
			r = '?'
		}
		out = append(out, r)
	}
	return string(out)
}
