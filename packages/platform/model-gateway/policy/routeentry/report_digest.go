package routeentry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// O DIGEST DE UM RELATÓRIO DE QUALIFICAÇÃO (AOS-518; vive aqui desde o AOS-519).
//
// Um relatório de qualificação do banco de ensaio traz, no campo `digest_do_relatorio` do topo, o
// sha256 de si próprio. É o que um registo de entrada assinado cita e o que o nó confere. O
// cálculo vive NESTE pacote, e não no banco, porque são dois os que o fazem — o banco, que escreve
// o relatório, e o nó, que o confere — e o nó não importa código de ensaio. O banco delega aqui:
// há uma só escrita da forma canónica.
//
// SOBRE QUE BYTES. O digest calcula-se sobre a FORMA CANÓNICA do documento JSON do relatório
// ([CanonicalReport]), que se obtém do ficheiro assim:
//
//  1. lê-se o ficheiro como UM documento JSON em UTF-8 válido cujo topo é um objecto; um nome de
//     membro repetido no mesmo objecto, em qualquer profundidade, recusa — e «repetido» conta sem
//     distinguir maiúsculas de minúsculas (dois leitores podiam ler valores diferentes do mesmo
//     ficheiro); um texto ou um nome com U+FFFD, ou com um escape de meio par substituto isolado
//     (que o leitor trocaria por U+FFFD), também recusa;
//  2. retira-se do objecto do topo o membro `digest_do_relatorio` — e só esse, e só no topo;
//  3. escreve-se o documento sem espaços nem quebras de linha: os membros de cada objecto pela
//     ordem dos BYTES do nome (UTF-8); as listas pela ordem em que estão; os números com o
//     literal EXACTAMENTE como está no ficheiro; `true`, `false` e `null` tal e qual; e os textos
//     como o `encoding/json` do Go os escreve com o escape de HTML desligado.
//
// O digest é `sha256:` seguido do sha256 desses bytes em hexadecimal.
//
// O QUE ISTO DÁ: mudar uma contagem, uma razão, o veredicto ou qualquer outro valor muda o digest;
// mudar só a indentação ou a ordem dos membros não muda.
//
// O QUE NÃO DÁ: o digest dá INTEGRIDADE, não AUTENTICIDADE. Quem altera o veredicto pode
// recalcular o digest e pô-lo no ficheiro: a verificação passa. A autenticidade vem da assinatura
// do registo de entrada que CITA o digest ([VerifySignature]).

// ReportDigestField é o nome do membro do topo que guarda o digest do relatório.
const ReportDigestField = "digest_do_relatorio"

// ErrReportDigest — o relatório não tem a forma em que o digest se calcula, não traz digest, ou o
// digest que traz não é o seu.
var ErrReportDigest = errors.New("digest do relatorio de qualificacao")

// CanonicalReport devolve a forma canónica de um relatório em JSON, sem o membro do digest.
func CanonicalReport(raw []byte) ([]byte, error) {
	canonical, _, err := canonicalize(raw)
	return canonical, err
}

// ReportDigest calcula o digest de um relatório em JSON: o sha256 da sua forma canónica.
func ReportDigest(raw []byte) (string, error) {
	canonical, _, err := canonicalize(raw)
	if err != nil {
		return "", err
	}
	return sha256Of(canonical), nil
}

// VerifyReportDigest confere que o digest que o relatório traz é o da sua forma canónica. Devolve
// o digest. Um relatório sem o campo, ou com outro valor, é [ErrReportDigest].
func VerifyReportDigest(raw []byte) (string, error) {
	canonical, declared, err := canonicalize(raw)
	if err != nil {
		return "", err
	}
	if declared == "" {
		return "", fmt.Errorf("%w: o relatorio nao traz o campo %s", ErrReportDigest, ReportDigestField)
	}
	if sha256Of(canonical) != declared {
		return "", fmt.Errorf("%w: o relatorio foi alterado (o digest que traz nao e o do seu conteudo)", ErrReportDigest)
	}
	return declared, nil
}

// sha256Of devolve `sha256:` seguido do sha256 de b em hexadecimal.
func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalize devolve a forma canónica e o valor do membro do digest (vazio se não houver).
func canonicalize(raw []byte) (canonical []byte, declared string, err error) {
	if !utf8.Valid(raw) {
		// O descodificador trocaria os bytes inválidos por U+FFFD em silêncio: dois ficheiros
		// diferentes passavam a ter a mesma forma canónica.
		return nil, "", fmt.Errorf("%w: o relatorio nao e UTF-8 valido", ErrReportDigest)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return nil, "", fmt.Errorf("%w: o relatorio nao e um objecto JSON", ErrReportDigest)
	}
	var b bytes.Buffer
	declared, err = canonicalObject(dec, &b, true)
	if err != nil {
		return nil, "", err
	}
	if _, end := dec.Token(); end != io.EOF {
		return nil, "", fmt.Errorf("%w: ha dados depois do documento", ErrReportDigest)
	}
	return b.Bytes(), declared, nil
}

// canonicalObject escreve um objecto cujo `{` já foi lido. No topo retira o membro do digest e
// devolve o seu valor.
func canonicalObject(dec *json.Decoder, b *bytes.Buffer, top bool) (string, error) {
	members := map[string][]byte{}
	declared := ""
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("%w: JSON ilegivel", ErrReportDigest)
		}
		name, _ := t.(string)
		// Repetido conta SEM distinguir maiúsculas: o `encoding/json` do Go casa `Veredicto` com
		// o campo `veredicto`, e um leitor desses leria o valor do membro que viesse por último.
		for seen := range members {
			if strings.EqualFold(seen, name) {
				return "", fmt.Errorf("%w: nome de membro repetido", ErrReportDigest)
			}
		}
		if top && strings.EqualFold(name, ReportDigestField) && (declared != "" || name != ReportDigestField) {
			return "", fmt.Errorf("%w: nome de membro repetido", ErrReportDigest)
		}
		var value bytes.Buffer
		if err := canonicalValue(dec, &value); err != nil {
			return "", err
		}
		if top && name == ReportDigestField {
			var s string
			if json.Unmarshal(value.Bytes(), &s) != nil || s == "" {
				return "", fmt.Errorf("%w: o campo %s nao e um texto", ErrReportDigest, ReportDigestField)
			}
			declared = s
			continue
		}
		members[name] = value.Bytes()
	}
	if _, err := dec.Token(); err != nil { // o `}`
		return "", fmt.Errorf("%w: JSON ilegivel", ErrReportDigest)
	}
	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := writeCanonicalText(b, n); err != nil {
			return "", err
		}
		b.WriteByte(':')
		b.Write(members[n])
	}
	b.WriteByte('}')
	return declared, nil
}

// canonicalValue lê o valor seguinte e escreve-o na forma canónica.
func canonicalValue(dec *json.Decoder, b *bytes.Buffer) error {
	t, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: JSON ilegivel", ErrReportDigest)
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			_, err := canonicalObject(dec, b, false)
			return err
		case '[':
			b.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := canonicalValue(dec, b); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // o `]`
				return fmt.Errorf("%w: JSON ilegivel", ErrReportDigest)
			}
			b.WriteByte(']')
			return nil
		}
		return fmt.Errorf("%w: JSON ilegivel", ErrReportDigest)
	case json.Number:
		b.WriteString(v.String())
	case string:
		return writeCanonicalText(b, v)
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	default:
		return fmt.Errorf("%w: JSON ilegivel", ErrReportDigest)
	}
	return nil
}

// writeCanonicalText escreve um texto como o `encoding/json` o escreve, sem o escape de HTML.
func writeCanonicalText(b *bytes.Buffer, s string) error {
	if strings.ContainsRune(s, utf8.RuneError) {
		// Um escape de meio par substituto isolado (de D800 a DFFF) chega aqui trocado por U+FFFD:
		// textos diferentes no ficheiro dariam a mesma forma canónica. Recusa-se o carácter.
		return fmt.Errorf("%w: texto com um caracter de substituicao (U+FFFD) ou um par substituto partido", ErrReportDigest)
	}
	var t bytes.Buffer
	enc := json.NewEncoder(&t)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("%w: texto que nao se escreve em JSON", ErrReportDigest)
	}
	b.Write(bytes.TrimRight(t.Bytes(), "\n"))
	return nil
}
