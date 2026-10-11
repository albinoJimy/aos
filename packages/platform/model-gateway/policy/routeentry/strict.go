package routeentry

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A LEITURA FECHADA DE JSON.
//
// O `encoding/json` do Go, a ler para uma struct, faz três coisas que aqui não podem acontecer:
// casa `"MODO"` com o campo `modo`; de dois membros com o mesmo nome fica com o último; e ignora
// os membros que não conhece. Qualquer uma delas faz com que o que o nó lê não seja o que quem
// assinou julgou ter assinado. Por isso os documentos lêem-se para um mapa de nomes EXACTOS, com
// os repetidos recusados (também os que só diferem na caixa), e quem lê confere depois o conjunto
// de nomes contra uma lista fechada ([onlyKeys]).

// strictObject lê `raw` como UM objecto JSON e devolve os seus membros pelo nome exacto. Recusa
// (false): bytes que não sejam UTF-8 válido, um topo que não seja um objecto, um nome repetido —
// sem distinguir maiúsculas de minúsculas —, e conteúdo depois do objecto.
func strictObject(raw []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if first, err := dec.Token(); err != nil || first != json.Delim('{') {
		return nil, false
	}
	members := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, isText := t.(string)
		if !isText {
			return nil, false
		}
		for seen := range members {
			if strings.EqualFold(seen, name) {
				return nil, false
			}
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		members[name] = value
	}
	if _, err := dec.Token(); err != nil { // o `}`
		return nil, false
	}
	if _, end := dec.Token(); end != io.EOF {
		return nil, false
	}
	return members, true
}

// onlyKeys diz se todos os nomes de `members` estão em `allowed`. A comparação é exacta: `Modo`
// não é `modo`.
func onlyKeys(members map[string]json.RawMessage, allowed []string) bool {
	for name := range members {
		known := false
		for _, a := range allowed {
			known = known || a == name
		}
		if !known {
			return false
		}
	}
	return true
}

// textOf lê um membro que tem de ser um texto JSON. Um número, um booleano, `null`, uma lista ou
// um objecto recusam; um texto com o carácter de substituição também (um escape partido chega
// trocado por ele, e dois textos diferentes passavam a ser o mesmo).
func textOf(raw json.RawMessage) (string, bool) {
	v := bytes.TrimSpace(raw)
	if len(v) < 2 || v[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil || strings.ContainsRune(s, utf8.RuneError) {
		return "", false
	}
	return s, true
}

// boolOf lê um membro que tem de ser `true` ou `false`, escritos assim.
func boolOf(raw json.RawMessage) (value, ok bool) {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// intOf lê um membro que tem de ser um literal inteiro JSON sem fracção nem expoente (`10`, e não
// `10.0`, `1e1` nem `"10"`), com no máximo 9 algarismos.
func intOf(raw json.RawMessage) (int, bool) {
	v := string(bytes.TrimSpace(raw))
	digits := strings.TrimPrefix(v, "-")
	if digits == "" || len(digits) > 9 || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// validDigest diz se s tem a forma `sha256:` seguido de 64 algarismos hexadecimais minúsculos.
func validDigest(s string) bool {
	hexa, has := strings.CutPrefix(s, "sha256:")
	if !has || len(hexa) != 64 {
		return false
	}
	for i := 0; i < len(hexa); i++ {
		c := hexa[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// maxLabel é o tecto em bytes de um rótulo de registo e de um nome pedido.
const maxLabel = 64

// validLabel diz se s serve de rótulo de um registo (o nome do ficheiro, sem o sufixo) e de nome
// pedido de um perfil assinado: de 1 a 64 letras, dígitos, `_` e `-`. Sem ponto — o nome pedido
// entra num nome de stream do Event Store quando a escada de tiers está armada (AOS-425) —, e sem
// nada que precise de escape num banner, num rótulo de métrica ou num caminho.
func validLabel(s string) bool {
	if s == "" || len(s) > maxLabel {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
