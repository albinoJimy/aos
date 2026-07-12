package replay

import (
	"reflect"
	"strconv"
	"strings"
)

// itoa é um atalho local para os testes.
func itoa(n int) string { return strconv.Itoa(n) }

// sanitize troca espaços e parênteses por '_' para compor run_ids de teste.
func sanitize(s string) string {
	r := strings.NewReplacer(" ", "_", "(", "", ")", "", ":", "")
	return r.Replace(s)
}

// typeInspector é um auxiliar de reflexão para asserções estruturais sobre os
// campos de um struct (prova de zero-efeitos: ausência de campos de efeito ao vivo).
type typeInspector struct {
	t reflect.Type
}

func reflectType(v any) typeInspector {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return typeInspector{t: t}
}

// hasFieldType indica se algum campo do struct tem um tipo cujo nome contém `name`.
func (ti typeInspector) hasFieldType(name string) bool {
	if ti.t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < ti.t.NumField(); i++ {
		ft := ti.t.Field(i).Type
		tn := ft.String()
		if ft.Name() != "" {
			tn = ft.Name()
		}
		if strings.Contains(ft.String(), name) || strings.Contains(tn, name) {
			return true
		}
	}
	return false
}
