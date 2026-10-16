// Package routeentrytest monta registos de entrada de perfis de rota PARA TESTES (AOS-519): uma
// chave gerada no teste, um relatório de qualificação com a forma do banco de ensaio, o registo,
// a assinatura e a pasta. Serve os testes do pacote `routeentry`, os do nó e os do `aos-issuer`,
// que precisam todos do mesmo registo válido para lhe estragar uma coisa de cada vez.
//
// NÃO É CÓDIGO DE PRODUÇÃO, e nenhum binário o importa. O relatório que aqui se monta diz
// `modo: real` e `vale_para_producao: true` sem corrida nenhuma por trás: é a prova de que o
// digest do relatório dá integridade e não autenticidade — o que o torna inútil para um atacante
// é a assinatura do registo, feita com uma chave que só existe no teste.
package routeentrytest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// Board e Region são o board e a região da allowlist EMBEBIDA do gateway — os de um nó que não
// monta allowlist externa.
const (
	Board  = "board-eu"
	Region = "eu"
)

// ProfileJSON é um perfil de rota para um alias que a allowlist embebida autoriza (`gpt-4o`),
// com um modelo esperado que a tabela em código NÃO conhece, com `/` e com `.`.
const ProfileJSON = `{"requested":"gpt-4o","expected_model":"openai/modelo-novo-4.5","wire_class":"openai-chat-completions","capabilities":["tools"]}`

// Key é um par de chaves de assinante gerado no teste.
type Key struct {
	Pub  ed25519.PublicKey
	Priv ed25519.PrivateKey
}

// NewKey gera um par de chaves.
func NewKey(t testing.TB) Key {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Key{Pub: pub, Priv: priv}
}

// Anchor devolve a chave pública em base64, como vai na âncora de confiança.
func (k Key) Anchor() string { return base64.StdEncoding.EncodeToString(k.Pub) }

// ProfileDigest devolve o digest do perfil escrito em `profileJSON`, como o nó o calcula.
func ProfileDigest(t testing.TB, profileJSON string) string {
	t.Helper()
	p, err := modelgateway.ParseRouteProfile([]byte(profileJSON))
	if err != nil {
		t.Fatalf("perfil de teste invalido: %v", err)
	}
	return p.Digest()
}

// ReportDoc devolve os membros de um relatório de qualificação com veredicto `qualificado`, do
// modo `real`, para o perfil com o digest dado, atrás da imagem do proxy declarada para produção.
// O teste muda-lhe o que quiser antes de o selar com [SealReport].
func ReportDoc(profileDigest string) map[string]any {
	return map[string]any{
		"versao_do_relatorio": routeentry.ReportVersion,
		"ticket":              "AOS-518",
		"data_utc":            "2026-10-11T12:00:00Z",
		"modo":                routeentry.ModeReal,
		"experiencia":         routeentry.ExperimentQualification,
		"terminou":            "completa",
		"qualificacao_do_modelo": map[string]any{
			"veredicto":          routeentry.VerdictQualified,
			"razoes":             []string{},
			"avisos":             []string{},
			"regra":              "regra de teste",
			"vale_para_producao": true,
			"rota_qualificada": map[string]any{
				"fornecedor":                "fornecedor-de-teste",
				"modelo":                    "modelo-novo-4.5",
				"modelo_esperado":           "openai/modelo-novo-4.5",
				"digest_do_perfil":          profileDigest,
				"imagem_do_proxy":           routeentry.ProductionProxyImage,
				"digest_da_imagem_do_proxy": routeentry.ProductionProxyImageDigest(),
			},
		},
	}
}

// Qualification devolve o bloco `qualificacao_do_modelo` de um [ReportDoc], para o teste o mudar.
func Qualification(doc map[string]any) map[string]any {
	return doc["qualificacao_do_modelo"].(map[string]any)
}

// QualifiedRoute devolve o bloco `rota_qualificada` de um [ReportDoc].
func QualifiedRoute(doc map[string]any) map[string]any {
	return Qualification(doc)["rota_qualificada"].(map[string]any)
}

// SealReport escreve o relatório em JSON com o seu digest no campo próprio, e devolve os bytes e o
// digest.
func SealReport(t testing.TB, doc map[string]any) ([]byte, string) {
	t.Helper()
	delete(doc, routeentry.ReportDigestField)
	semDigest, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := routeentry.ReportDigest(semDigest)
	if err != nil {
		t.Fatal(err)
	}
	doc[routeentry.ReportDigestField] = digest
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw, digest
}

// EntryDoc devolve os membros de um registo de entrada para o perfil e o relatório dados.
func EntryDoc(t testing.TB, profileJSON, board, region, reportDigest string) map[string]any {
	t.Helper()
	return map[string]any{
		"formato":                   routeentry.EntryFormat,
		"board":                     board,
		"regiao":                    region,
		"perfil":                    json.RawMessage(profileJSON),
		"digest_do_perfil":          ProfileDigest(t, profileJSON),
		"digest_do_relatorio":       reportDigest,
		"digest_da_imagem_do_proxy": routeentry.ProductionProxyImageDigest(),
		"fatia_inicial_percentagem": 10,
	}
}

// Marshal escreve um documento em JSON.
func Marshal(t testing.TB, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Write escreve na pasta os ficheiros de um registo. Um argumento nil não é escrito.
func Write(t testing.TB, dir, label string, entry []byte, signature string, report []byte) {
	t.Helper()
	files := map[string][]byte{routeentry.SuffixEntry: entry, routeentry.SuffixReport: report}
	if signature != "" {
		files[routeentry.SuffixSignature] = []byte(signature + "\n")
	}
	for suffix, content := range files {
		if content == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, label+suffix), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Good escreve na pasta um registo VÁLIDO para o perfil dado, assinado com a chave dada, e
// devolve os bytes do registo e do relatório.
func Good(t testing.TB, dir, label, profileJSON, board, region string, key Key) (entry, report []byte) {
	t.Helper()
	report, reportDigest := SealReport(t, ReportDoc(ProfileDigest(t, profileJSON)))
	entry = Marshal(t, EntryDoc(t, profileJSON, board, region, reportDigest))
	Write(t, dir, label, entry, routeentry.Sign(entry, key.Priv), report)
	return entry, report
}

// ToolProfileJSON é um perfil de rota para um registo de uso `tool` (AOS-530): um alias que a
// allowlist embebida autoriza (`gpt-4o-mini`), com a capacidade `imagem` e sem `tools`.
const ToolProfileJSON = `{"requested":"gpt-4o-mini","expected_model":"openrouter/anthropic/modelo-de-visao","wire_class":"openai-chat-completions","capabilities":["imagem"]}`

// OCRReportDoc devolve os membros de um relatório da experiência `ocr` com veredicto
// `qualificado`, do modo `real`, para o perfil com o digest dado, atrás da imagem do proxy
// declarada para produção — a forma que o banco escreve (AOS-530). O teste muda-lhe o que quiser
// antes de o selar com [SealReport].
func OCRReportDoc(profileDigest string) map[string]any {
	return map[string]any{
		"versao_do_relatorio": routeentry.ReportVersion,
		"experiencia":         routeentry.ExperimentOCR,
		"data_utc":            "2026-10-15T12:00:00Z",
		"modo":                routeentry.ModeReal,
		"motor":               "visao",
		"imagens":             []any{},
		"veredicto_do_ocr": map[string]any{
			"veredicto":            routeentry.VerdictQualified,
			"razoes":               []string{},
			"regra":                "regra de teste",
			"limiar_de_factos":     0.9,
			"factos":               10,
			"presentes_pela_regra": 10,
			"vale_para_producao":   true,
			"rota_medida": map[string]any{
				"fornecedor":                "fornecedor-de-teste",
				"modelo":                    "modelo-de-visao",
				"modelo_esperado":           "openrouter/anthropic/modelo-de-visao",
				"digest_do_perfil":          profileDigest,
				"imagem_do_proxy":           routeentry.ProductionProxyImage,
				"digest_da_imagem_do_proxy": routeentry.ProductionProxyImageDigest(),
			},
		},
	}
}

// OCRVerdict devolve o bloco `veredicto_do_ocr` de um [OCRReportDoc].
func OCRVerdict(doc map[string]any) map[string]any {
	return doc["veredicto_do_ocr"].(map[string]any)
}

// OCRMeasuredRoute devolve o bloco `rota_medida` de um [OCRReportDoc].
func OCRMeasuredRoute(doc map[string]any) map[string]any {
	return OCRVerdict(doc)["rota_medida"].(map[string]any)
}

// ToolEntryDoc devolve os membros de um registo de entrada de uso `tool`: o de [EntryDoc] com
// `uso: tool` e a fatia a 0.
func ToolEntryDoc(t testing.TB, profileJSON, board, region, reportDigest string) map[string]any {
	t.Helper()
	d := EntryDoc(t, profileJSON, board, region, reportDigest)
	d["uso"] = routeentry.UseTool
	d["fatia_inicial_percentagem"] = 0
	return d
}

// GoodTool escreve na pasta um registo VÁLIDO de uso `tool` para o perfil dado, a citar um
// relatório `ocr` qualificado, assinado com a chave dada, e devolve os bytes do registo e do
// relatório.
func GoodTool(t testing.TB, dir, label, profileJSON, board, region string, key Key) (entry, report []byte) {
	t.Helper()
	report, reportDigest := SealReport(t, OCRReportDoc(ProfileDigest(t, profileJSON)))
	entry = Marshal(t, ToolEntryDoc(t, profileJSON, board, region, reportDigest))
	Write(t, dir, label, entry, routeentry.Sign(entry, key.Priv), report)
	return entry, report
}
