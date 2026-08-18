package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// ------------------------------------------------------------------------------------------
// LIGAÇÃO DA ASSERÇÃO HUMANA À DELEGAÇÃO CONCRETA (fecha o eixo de ADR-003).
//
// O PROBLEMA que isto resolve, e que `--assertion` sozinho NÃO resolvia: um ID-token prova que
// o humano SE AUTENTICOU. Não diz nada sobre QUE agente, com QUE capabilities, por QUANTO tempo.
// Quem detivesse a chave do issuer MAIS um ID-token fresco do humano podia cunhar QUALQUER NHI
// enraizada nele. Trocar --human por --assertion subia de "declarado" para "esteve presente" —
// não para "autorizou ISTO", que é o que o ADR-003 exige.
//
// A ligação faz-se pelo `nonce` do fluxo de código de autorização: quem pede a autenticação
// escolhe o nonce, o IdP ECOA-O no ID-token, e o verificador compara. Se o nonce for um digest
// dos PARÂMETROS DA DELEGAÇÃO, então o token só é válido para a delegação que o humano tinha à
// frente quando se autenticou. Um token capturado deixa de servir para cunhar OUTRA coisa.
//
// O issuer CALCULA o nonce esperado a partir das flags que está a cunhar — não o aceita por
// parâmetro. É a diferença entre verificar e ser-se dito: um nonce fornecido ao lado dos
// parâmetros seria trivialmente feito coincidir com o que quer que se estivesse a cunhar.
// ------------------------------------------------------------------------------------------

// delegationBindingDomain versiona o digest. À cabeça e com length-prefix como todo o resto:
// mudar o esquema muda o domínio, e um nonce do esquema antigo deixa de coincidir em vez de
// coincidir por acidente.
const delegationBindingDomain = "aos-delegation-binding/v1"

// assertionMaxAge é o tecto de idade do ID-token humano no mint, medido por `iat`. Igual ao do
// read-path soberano e ao do directório humano do nó — deixar este caminho sem tecto tornava-o o
// elo mais fraco dos três.
const assertionMaxAge = 5 * time.Minute

// delegationNonce devolve o nonce que o humano TEM de ter trazido no ID-token para que esta
// emissão concreta seja autorizada por ele.
//
// Codificação INJECTIVA por length-prefix (uvarint), o mesmo molde de hitl/encode.go, e pela
// mesma razão: com um separador simples, (agent="a", class="bc") e (agent="ab", class="c")
// produziriam os MESMOS bytes. Um atacante que controlasse um campo podia deslizar a fronteira
// para o seguinte e obter um digest que o humano autorizou para outra coisa.
//
// As capabilities são ORDENADAS antes do digest: o humano autoriza um CONJUNTO de poderes, e
// "cap:a,cap:b" não é uma autorização diferente de "cap:b,cap:a". Sem a ordenação, reordenar a
// flag daria um mismatch que pareceria um ataque e era um acidente.
//
// O `--issuer` NÃO entra: não é algo que o humano autorize (é a identidade de quem assina), e um
// issuer id trocado já é recusado pelo nó contra o seu AOS_ISSUER_ID.
func delegationNonce(agent, class string, caps []string, ttl time.Duration) string {
	normalizadas := make([]string, 0, len(caps))
	for _, c := range caps {
		if c = strings.TrimSpace(c); c != "" {
			normalizadas = append(normalizadas, c)
		}
	}
	sort.Strings(normalizadas)

	buf := make([]byte, 0, 256)
	buf = putStr(buf, delegationBindingDomain)
	buf = putStr(buf, agent)
	buf = putStr(buf, class)
	buf = putU64(buf, uint64(len(normalizadas))) // a CARDINALIDADE também é prefixo
	for _, c := range normalizadas {
		buf = putStr(buf, c)
	}
	buf = putU64(buf, uint64(ttl)) // nanossegundos; um TTL maior é mais poder, logo entra

	soma := sha256.Sum256(buf)
	// base64url sem padding: o nonce viaja num query param do pedido de autorização.
	return base64.RawURLEncoding.EncodeToString(soma[:])
}

// cmdDelegationNonce imprime o nonce que o pedido de autenticação do humano tem de levar para
// que o `mint` com os MESMOS parâmetros o aceite.
//
// Existe para que o cliente que conduz o fluxo de browser (get-id-token.ps1) NÃO reimplemente o
// digest. Duas implementações do mesmo cálculo divergem — e a divergência aqui não daria um erro
// legível, daria um "nonce nao corresponde" que parece um ataque e é um bug de portabilidade
// (ordem de campos, codificação de texto, unidade do TTL). Uma fonte só, invocada pelos dois
// lados: se mudar, muda para ambos.
func cmdDelegationNonce(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("delegation-nonce", flag.ContinueOnError)
	agent := fs.String("agent", "", "id do agente (NHI a criar)")
	class := fs.String("class", "", "classe do agente")
	caps := fs.String("caps", "", "capabilities CSV")
	ttl := fs.Duration("ttl", 15*time.Minute, "TTL do token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" || *class == "" {
		return errors.New("delegation-nonce exige --agent e --class")
	}
	_, err := fmt.Fprintln(out, delegationNonce(*agent, *class, splitCSV(*caps), *ttl))
	return err
}

func putStr(buf []byte, s string) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(s)))
	buf = append(buf, lb[:n]...)
	return append(buf, s...)
}

func putU64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}

// ------------------------------------------------------------------------------------------
// Transporte para o IdP.
// ------------------------------------------------------------------------------------------

// ErrIdPCASemPEM — o ficheiro indicado em --oidc-ca não contém certificado PEM nenhum.
var ErrIdPCASemPEM = errors.New("aos-issuer: --oidc-ca sem certificado PEM")

// idpHTTPClient constrói o cliente com que o verificador alcança o IdP. caPath vazio ⇒ nil, e o
// verificador usa o seu default endurecido (AOS-229: TLS 1.2 + timeout + limite de redirects +
// anti-SSRF) contra a trust store do sistema.
//
// A flag existe pela mesma razão que --vault-ca já existia neste binário: um IdP servido por uma
// CA PRIVADA é indistinguível de um IdP forjado para quem só conheça as CA públicas, e o binário
// não honra SSL_CERT_FILE em todas as plataformas. Confia-se na CA EXPLICITAMENTE, nunca se
// desliga a verificação — o JWKS que daqui vem é o que decide se o humano é quem diz ser.
func idpHTTPClient(caPath string) (*http.Client, error) {
	if strings.TrimSpace(caPath) == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("ler CA do IdP: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%w: %q", ErrIdPCASemPEM, caPath)
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}, nil
}
