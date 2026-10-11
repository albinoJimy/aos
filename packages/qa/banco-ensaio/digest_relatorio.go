package bancoensaio

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

// O DIGEST DO PRÓPRIO RELATÓRIO (AOS-518).
//
// Um relatório de qualificação traz, no campo `digest_do_relatorio` do topo, o sha256 de si
// próprio. É o que um perfil assinado cita e o que o nó confere (AOS-519).
//
// SOBRE QUE BYTES. O digest calcula-se sobre a FORMA CANÓNICA do documento JSON do relatório
// ([FormaCanonicaDoRelatorio]), que se obtém do ficheiro assim:
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
//     como o `encoding/json` do Go os escreve com o escape de HTML desligado — entre aspas, com
//     `\"`, `\\`, `\n`, `\r`, `\t`, `\u00XX` para os outros caracteres de controlo, ` ` e
//     ` `, e tudo o resto em UTF-8 directo.
//
// O digest é `sha256:` seguido do sha256 desses bytes em hexadecimal.
//
// O QUE ISTO DÁ: mudar uma contagem, uma razão, o veredicto ou qualquer outro valor muda o digest;
// mudar só a indentação ou a ordem dos membros não muda. O resumo em texto (`.txt`) NÃO é coberto:
// é derivado do JSON, e é o JSON que se confere.
//
// O QUE NÃO DÁ: o digest dá INTEGRIDADE, não AUTENTICIDADE. Quem altera o veredicto pode
// recalcular o digest e pô-lo no ficheiro: a verificação passa. O digest serve para um registo
// ASSINADO citar um relatório exacto (AOS-519) — é a assinatura desse registo que diz quem
// responde por ele. E não prova que a corrida aconteceu: o relatório não é assinado pelo banco.

// CampoDoDigestDoRelatorio é o nome do membro do topo que guarda o digest do relatório.
const CampoDoDigestDoRelatorio = "digest_do_relatorio"

// ErrDigestDoRelatorio — o relatório não tem a forma em que o digest se calcula, não traz digest,
// ou o digest que traz não é o seu.
var ErrDigestDoRelatorio = errors.New("banco-ensaio: digest do relatorio")

// FormaCanonicaDoRelatorio devolve a forma canónica de um relatório em JSON, sem o membro do digest.
func FormaCanonicaDoRelatorio(cru []byte) ([]byte, error) {
	canonico, _, err := canonizarRelatorio(cru)
	return canonico, err
}

// DigestDoRelatorio calcula o digest de um relatório em JSON: o sha256 da sua forma canónica.
func DigestDoRelatorio(cru []byte) (string, error) {
	canonico, _, err := canonizarRelatorio(cru)
	if err != nil {
		return "", err
	}
	soma := sha256.Sum256(canonico)
	return "sha256:" + hex.EncodeToString(soma[:]), nil
}

// VerificarDigestDoRelatorio confere que o digest que o relatório traz é o da sua forma canónica.
// Devolve o digest. Um relatório sem o campo, ou com outro valor, é [ErrDigestDoRelatorio].
func VerificarDigestDoRelatorio(cru []byte) (string, error) {
	canonico, declarado, err := canonizarRelatorio(cru)
	if err != nil {
		return "", err
	}
	if declarado == "" {
		return "", fmt.Errorf("%w: o relatorio nao traz o campo %s", ErrDigestDoRelatorio, CampoDoDigestDoRelatorio)
	}
	soma := sha256.Sum256(canonico)
	if calculado := "sha256:" + hex.EncodeToString(soma[:]); calculado != declarado {
		return "", fmt.Errorf("%w: o relatorio foi alterado (o digest que traz nao e o do seu conteudo)", ErrDigestDoRelatorio)
	}
	return declarado, nil
}

// canonizarRelatorio devolve a forma canónica e o valor do membro do digest (vazio se não houver).
func canonizarRelatorio(cru []byte) (canonico []byte, declarado string, err error) {
	if !utf8.Valid(cru) {
		// O descodificador trocaria os bytes inválidos por U+FFFD em silêncio: dois ficheiros
		// diferentes passavam a ter a mesma forma canónica.
		return nil, "", fmt.Errorf("%w: o relatorio nao e UTF-8 valido", ErrDigestDoRelatorio)
	}
	dec := json.NewDecoder(bytes.NewReader(cru))
	dec.UseNumber()
	inicio, err := dec.Token()
	if err != nil || inicio != json.Delim('{') {
		return nil, "", fmt.Errorf("%w: o relatorio nao e um objecto JSON", ErrDigestDoRelatorio)
	}
	var b bytes.Buffer
	declarado, err = canonizarObjecto(dec, &b, true)
	if err != nil {
		return nil, "", err
	}
	if _, fim := dec.Token(); fim != io.EOF {
		return nil, "", fmt.Errorf("%w: ha dados depois do documento", ErrDigestDoRelatorio)
	}
	return b.Bytes(), declarado, nil
}

// canonizarObjecto escreve um objecto cujo `{` já foi lido. No topo retira o membro do digest e
// devolve o seu valor.
func canonizarObjecto(dec *json.Decoder, b *bytes.Buffer, topo bool) (string, error) {
	membros := map[string][]byte{}
	declarado := ""
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("%w: JSON ilegivel", ErrDigestDoRelatorio)
		}
		nome, _ := t.(string)
		// Repetido conta SEM distinguir maiúsculas: o `encoding/json` do Go casa `Veredicto` com
		// o campo `veredicto`, e um leitor desses leria o valor do membro que viesse por último.
		for visto := range membros {
			if strings.EqualFold(visto, nome) {
				return "", fmt.Errorf("%w: nome de membro repetido", ErrDigestDoRelatorio)
			}
		}
		if topo && strings.EqualFold(nome, CampoDoDigestDoRelatorio) && (declarado != "" || nome != CampoDoDigestDoRelatorio) {
			return "", fmt.Errorf("%w: nome de membro repetido", ErrDigestDoRelatorio)
		}
		var valor bytes.Buffer
		if err := canonizarValor(dec, &valor); err != nil {
			return "", err
		}
		if topo && nome == CampoDoDigestDoRelatorio {
			var s string
			if json.Unmarshal(valor.Bytes(), &s) != nil || s == "" {
				return "", fmt.Errorf("%w: o campo %s nao e um texto", ErrDigestDoRelatorio, CampoDoDigestDoRelatorio)
			}
			declarado = s
			continue
		}
		membros[nome] = valor.Bytes()
	}
	if _, err := dec.Token(); err != nil { // o `}`
		return "", fmt.Errorf("%w: JSON ilegivel", ErrDigestDoRelatorio)
	}
	nomes := make([]string, 0, len(membros))
	for n := range membros {
		nomes = append(nomes, n)
	}
	sort.Strings(nomes)
	b.WriteByte('{')
	for i, n := range nomes {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := escreverTextoCanonico(b, n); err != nil {
			return "", err
		}
		b.WriteByte(':')
		b.Write(membros[n])
	}
	b.WriteByte('}')
	return declarado, nil
}

// canonizarValor lê o valor seguinte e escreve-o na forma canónica.
func canonizarValor(dec *json.Decoder, b *bytes.Buffer) error {
	t, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: JSON ilegivel", ErrDigestDoRelatorio)
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			_, err := canonizarObjecto(dec, b, false)
			return err
		case '[':
			b.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := canonizarValor(dec, b); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // o `]`
				return fmt.Errorf("%w: JSON ilegivel", ErrDigestDoRelatorio)
			}
			b.WriteByte(']')
			return nil
		}
		return fmt.Errorf("%w: JSON ilegivel", ErrDigestDoRelatorio)
	case json.Number:
		b.WriteString(v.String())
	case string:
		return escreverTextoCanonico(b, v)
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	default:
		return fmt.Errorf("%w: JSON ilegivel", ErrDigestDoRelatorio)
	}
	return nil
}

// escreverTextoCanonico escreve um texto como o `encoding/json` o escreve, sem o escape de HTML.
func escreverTextoCanonico(b *bytes.Buffer, s string) error {
	if strings.ContainsRune(s, utf8.RuneError) {
		// Um escape de meio par substituto isolado (de D800 a DFFF) chega aqui trocado por U+FFFD:
		// textos diferentes no ficheiro dariam a mesma forma canónica. Recusa-se o carácter.
		return fmt.Errorf("%w: texto com um caracter de substituicao (U+FFFD) ou um par substituto partido", ErrDigestDoRelatorio)
	}
	var t bytes.Buffer
	enc := json.NewEncoder(&t)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("%w: texto que nao se escreve em JSON", ErrDigestDoRelatorio)
	}
	b.Write(bytes.TrimRight(t.Bytes(), "\n"))
	return nil
}
