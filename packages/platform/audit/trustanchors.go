package audit

// trustanchors.go — A FORMA DO REGISTO DAS ÂNCORAS DE CONFIANÇA (AOS-446 fase 1, ADR-033 §8).
//
// # PORQUE É QUE ISTO VIVE AQUI
//
// O registo tem DOIS leitores em módulos diferentes, e é essa a razão de ser deste ficheiro:
//
//   - o NÓ escreve-o no arranque (`packages/cmd/aos/ancoras_de_confianca.go`), a partir da Config
//     composta — só ele sabe que âncoras estão em uso;
//   - o SELADOR lê-o fora do host (`packages/cmd/aos-issuer/wormseal.go`), a partir do ficheiro do
//     WORM que o backup trouxe — e é ele que denuncia a troca, porque a chave com que assina não
//     está no host onde a troca aconteceria.
//
// Se a forma tivesse duas definições, a de quem escreve e a de quem lê podiam divergir — e o
// sintoma seria o pior possível: o selador a não ver troca nenhuma, verde, a varrer nada. A forma
// vive no pacote que os dois módulos já importam; o CÁLCULO das impressões fica no nó, que é quem
// tem a configuração.
//
// O que aqui está é deliberadamente burro: um mapa `nome da âncora → impressão`, o seu digest, e a
// conversão de e para os parâmetros de uma obrigação. Nenhum conhecimento de chaves.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// TrustAnchorsPartition é a partição do WORM onde o registo vive. Nome próprio, e não `global`:
// a partição é a unidade de ancoragem do `worm-seal`, e o selo diário tem de poder cobrir ESTA
// história sem depender do que mais lá caia.
const TrustAnchorsPartition = "trust-anchors"

// Os dois tipos de registo. Existem os dois para que «mudou» não passe a significar «arrancou».
const (
	// TrustAnchorsChangedEventType — alguma âncora difere do último registo (ou é o primeiro).
	TrustAnchorsChangedEventType = "trust_anchors.changed"
	// TrustAnchorsActiveEventType — o arranque confirma as âncoras que já lá estavam.
	TrustAnchorsActiveEventType = "trust_anchors.active"
)

// TrustAnchorsDigestKey é o parâmetro com o digest do CONJUNTO; TrustAnchorsPrefix prefixa o de
// cada âncora. O prefixo é o que garante que nenhuma âncora se pode chamar `digest`.
const (
	TrustAnchorsDigestKey = "digest"
	TrustAnchorsPrefix    = "ancora."
)

// TrustAnchorsMaxSeq é o tecto da leitura da partição — o mesmo de `aos audit-trail`.
const TrustAnchorsMaxSeq = 1 << 40

// TrustAnchorAusente é o valor de uma âncora que NÃO está configurada. Explícito, e não o
// parâmetro em falta: «não há chave de política pinada» é um facto sobre a postura e tem de ser
// selado como tal — senão passar de «pinada» a «ausente» seria indistinguível de um binário que
// ainda não conhecia o campo.
const TrustAnchorAusente = "(ausente)"

// TrustAnchors é o retrato das âncoras em uso, já reduzido a impressões digitais: nome da âncora
// → impressão. Nunca material de chave.
type TrustAnchors map[string]string

// Digest resume o conjunto inteiro numa linha. É o valor que o `aos-issuer worm-seal` compara
// entre selagens, e o que o operador guarda.
//
// A ORDENAÇÃO É OBRIGATÓRIA: um mapa não tem ordem de iteração, e um digest que dependesse dela
// mudava de arranque para arranque — o registo acusaria uma troca a cada reinício, e ninguém
// voltaria a olhar para ele.
func (a TrustAnchors) Digest() string {
	nomes := make([]string, 0, len(a))
	for n := range a {
		nomes = append(nomes, n)
	}
	sort.Strings(nomes)
	h := sha256.New()
	h.Write([]byte("aos.trust-anchors.v1;"))
	for _, n := range nomes {
		fmt.Fprintf(h, "%d:%s=%d:%s;", len(n), n, len(a[n]), a[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Params converte o retrato para os parâmetros da obrigação selada.
func (a TrustAnchors) Params(actor string) map[string]string {
	p := make(map[string]string, len(a)+2)
	for n, v := range a {
		p[TrustAnchorsPrefix+n] = v
	}
	p[TrustAnchorsDigestKey] = a.Digest()
	p["actor"] = actor
	return p
}

// TrustAnchorsFromParams é o inverso, e vive ao lado da escrita para as duas não poderem
// divergir. Devolve o retrato e o digest tal como foram selados — o digest NÃO se recalcula aqui,
// porque comparar o recalculado com o recalculado não prova nada sobre o que ficou no registo.
func TrustAnchorsFromParams(p map[string]string) (TrustAnchors, string) {
	a := TrustAnchors{}
	for k, v := range p {
		if strings.HasPrefix(k, TrustAnchorsPrefix) {
			a[strings.TrimPrefix(k, TrustAnchorsPrefix)] = v
		}
	}
	return a, p[TrustAnchorsDigestKey]
}

// TrustAnchorsFromRecord extrai o retrato de um registo selado, se ele for um registo de âncoras.
func TrustAnchorsFromRecord(rec AuditRecord) (TrustAnchors, string, bool) {
	for _, o := range rec.Obligations {
		if o.Type == TrustAnchorsChangedEventType || o.Type == TrustAnchorsActiveEventType {
			a, d := TrustAnchorsFromParams(o.Params)
			return a, d, true
		}
	}
	return nil, "", false
}

// Diferencas nomeia as âncoras que mudaram face a `anterior`, em ordem estável. Uma âncora que só
// existe de um dos lados CONTA como diferença — é o caso de um binário que ganhou uma âncora
// nova, e é exactamente quando o operador precisa de ver a lista.
func (a TrustAnchors) Diferencas(anterior TrustAnchors) []string {
	nomes := map[string]bool{}
	for n := range a {
		nomes[n] = true
	}
	for n := range anterior {
		nomes[n] = true
	}
	ordenados := make([]string, 0, len(nomes))
	for n := range nomes {
		ordenados = append(ordenados, n)
	}
	sort.Strings(ordenados)
	var out []string
	for _, n := range ordenados {
		de, para := anterior[n], a[n]
		if de == para {
			continue
		}
		if de == "" {
			de = "(ausente do registo anterior)"
		}
		if para == "" {
			para = "(ausente deste arranque)"
		}
		out = append(out, n+": "+de+" -> "+para)
	}
	return out
}

// UltimasAncoras devolve o ÚLTIMO registo de âncoras da partição com `audit_seq ≤ ate`, e o seq
// em que está. `ate` a zero ⇒ sem tecto (o registo mais recente que houver).
//
// O TECTO EXISTE PARA O SELADOR. Ele precisa de duas leituras da MESMA partição: o que estava
// ancorado pela selagem anterior (tecto = o `audit_seq` daquele checkpoint, que a verificação
// ancorada já provou não ter mudado) e o que lá está agora. É a diferença entre os dois que
// denuncia a troca.
func UltimasAncoras(ctx context.Context, store Store, ate uint64) (TrustAnchors, string, uint64, bool, error) {
	if store == nil {
		return nil, "", 0, false, nil
	}
	tecto := ate
	if tecto == 0 {
		tecto = TrustAnchorsMaxSeq
	}
	recs, err := store.Read(ctx, TrustAnchorsPartition, 1, tecto)
	if err != nil {
		return nil, "", 0, false, err
	}
	for i := len(recs) - 1; i >= 0; i-- {
		if a, d, ok := TrustAnchorsFromRecord(recs[i]); ok {
			return a, d, recs[i].AuditSeq, true, nil
		}
	}
	return nil, "", 0, false, nil
}
