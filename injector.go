package rubik

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/rubikorg/rubik/internal/checker"
	"github.com/rubikorg/rubik/pkg"

	"github.com/pkg/errors"

	"github.com/julienschmidt/httprouter"
)

// inject is the the entry point of request injection in rubik
// an injection is a process of reading the
func inject(req *http.Request,
	pm httprouter.Params, en interface{}, v Validation) (interface{}, error) {
	// lets check what type of request it is
	ctype := req.Header.Get(Content.Header)
	var body = make(map[string]interface{})
	var params = make(map[string]string)
	// check if any params in the route
	if len(pm) > 0 {
		for _, p := range pm {
			exportedKey := capitalize(p.Key)
			params[exportedKey] = p.Value
		}
	}

	b, err := ioutil.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	switch ctype {
	case Content.JSON:
		err = json.Unmarshal(b, &body)
		if err != nil {
			return nil, err
		}
	case Content.URLEncoded:
		var encs url.Values
		encs, err = url.ParseQuery(string(b))
		// normalize the http.Values type to flat map
		for k, v := range encs {
			body[k] = v[0]
		}
	// TODO: need to evalueate multipart form data
	case Content.Multipart:
		err := req.ParseMultipartForm(32 << 20)
		if err != nil {
			return nil, err
		}
		break
	}

	values := reflect.ValueOf(en)
	fields := values.Elem().Type()
	num := values.Elem().NumField()

	for i := 0; i < num; i++ {
		field := fields.Field(i)

		if field.Name == "Entity" {
			continue
		}

		tag := field.Tag.Get(rubikTag)
		value := values.Elem().Field(i)
		transport := "query"
		transportKey := unCapitalize(field.Name)
		isRequired := false

		if strings.Contains(tag, "!") {
			isRequired = true
			tag = strings.ReplaceAll(tag, "!", "")
		}
		// get information from the tag
		if tag != "" {
			if strings.Contains(tag, "|") {
				reqTag := strings.Split(tag, "|")
				if reqTag[0] != "" {
					transportKey = reqTag[0]
				}
				if reqTag[1] != "" {
					transport = reqTag[1]
				}

			} else {
				if isOneOf(tag, "query", "body", "form", "param") {
					transport = tag
				} else {
					transportKey = tag
				}
			}
		}

		msg := "Data: %s is required but not found inside %s."
		requiredError := errors.New(fmt.Sprintf(msg, transportKey, transport))
		var val interface{}
		switch transport {
		case "query":
			val = req.URL.Query().Get(transportKey)
			if isRequired && (val == "" || val == nil) {
				return nil, requiredError
			}
			break
		case "body":
			val = body[transportKey]
			if (val == nil || val == "") && isRequired {
				return nil, requiredError
			}
			break
		case "form":
			files := req.MultipartForm.File[transportKey]
			if (files == nil || len(files) == 0) && isRequired {
				return nil, requiredError
			}
			val = files
			break
		case "param":
			paramKey := capitalize(strings.ToLower(transportKey))
			val = params[paramKey]
			if val == "" && isRequired {
				return nil, requiredError
			}
			break
		}

		// this is for the validations the developer provieded
		if len(v) > 0 && v[field.Name] != "" {
			err := checker.Check(value, v[field.Name])
			if err != nil {
				return nil, err
			}
		}

		injectValueByType(val, value, field.Type.Kind())
	}

	return values.Elem().Interface(), nil
}

func injectValueByType(val interface{}, elem reflect.Value, typ reflect.Kind) {
	switch typ {
	case reflect.String:
		final, ok := val.(string)
		if ok && elem.CanSet() {
			elem.SetString(final)
		}
		break
	case reflect.Int:
		value, _ := val.(string)
		if value == "" {
			return
		}

		final, ok := strconv.Atoi(value)
		if ok == nil && elem.CanSet() {
			elem.SetInt(int64(final))
		}
		break
	case reflect.Float32:
		value, _ := val.(string)
		if value == "" {
			return
		}
		// TODO: can make this conversion a function
		if floatVal, err := strconv.ParseFloat(value, 32); err == nil && elem.CanSet() {
			elem.SetFloat(floatVal)
		}
		break
	case reflect.Float64:
		value, _ := val.(string)
		if value == "" {
			return
		}
		// TODO: can make this conversion a function
		if floatVal, err := strconv.ParseFloat(value, 64); err == nil && elem.CanSet() {
			elem.SetFloat(floatVal)
		}
		break
	case reflect.Bool:
		value, _ := val.(string)
		if value == "" {
			return
		}

		boolean, err := strconv.ParseBool(value)
		if err == nil && elem.CanSet() {
			elem.SetBool(boolean)
		}
		break
	case reflect.Struct:
		// should we loop a on all struct fields and add value?
		break
	case reflect.Slice:
		break
	case reflect.TypeOf(File{}).Kind():
		// if it is a single file we coece it into []multipart.File first and
		// pick up the first one
		value, ok := val.([]*multipart.File)
		if ok && elem.CanSet() && len(value) > 0 {
			file := *(value)[0]
			defer file.Close()
			b, err := ioutil.ReadAll(file)
			if err != nil {
				pkg.ErrorMsg("error while reading form file: " + err.Error())
				return
			}
			elem.FieldByName("Raw").SetBytes(b)
		}
		break
	case reflect.TypeOf([]File{}).Kind():
		// TODO: the above File{} case and this can be a single function
		value, ok := val.([]*multipart.File)
		if ok && elem.CanSet() && len(value) > 0 {
			for _, f := range value {
				file := *f
				defer file.Close()

				b, err := ioutil.ReadAll(file)
				if err != nil {
					pkg.ErrorMsg("error while reading form file: " + err.Error())
					return
				}

				sliceElem := reflect.MakeSlice(elem.Type(), len(value), elem.Cap())
				for i := 0; i < len(value); i++ {
					rubikFile := reflect.New(reflect.TypeOf(File{}))
					rubikFile.FieldByName("Raw").SetBytes(b)
					sliceElem.Index(i).Set(reflect.ValueOf(rubikFile).Elem())
				}
			}
		}
		break
	}
}
