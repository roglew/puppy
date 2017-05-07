package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type SearchField int
type StrComparer int

type StrFieldGetter func(req *ProxyRequest) ([]string, error)
type KvFieldGetter func(req *ProxyRequest) ([]*PairValue, error)

type RequestChecker func(req *ProxyRequest) bool

// Searchable fields
const (
	FieldAll SearchField = iota

	FieldRequestBody
	FieldResponseBody
	FieldAllBody
	FieldWSMessage

	FieldRequestHeaders
	FieldResponseHeaders
	FieldBothHeaders

	FieldMethod
	FieldHost
	FieldPath
	FieldURL
	FieldStatusCode

	FieldBothParam
	FieldURLParam
	FieldPostParam
	FieldResponseCookie
	FieldRequestCookie
	FieldBothCookie
	FieldTag

	FieldAfter
	FieldBefore
	FieldTimeRange

	FieldInvert

	FieldId
)

// Operators for string values
const (
	StrIs StrComparer = iota
	StrContains
	StrContainsRegexp

	StrLengthGreaterThan
	StrLengthLessThan
	StrLengthEqualTo
)

// A struct representing the data to be searched for a pair such as a header or url param
type PairValue struct {
	key   string
	value string
}

type QueryPhrase [][]interface{} // A list of queries. Will match if any queries match the request
type MessageQuery []QueryPhrase  // A list of phrases. Will match if all the phrases match the request

type StrQueryPhrase [][]string
type StrMessageQuery []StrQueryPhrase

// Return a function that returns whether a request matches the given condition
func NewRequestChecker(args ...interface{}) (RequestChecker, error) {
	// Generates a request checker from the given search arguments
	if len(args) == 0 {
		return nil, errors.New("search requires a search field")
	}

	field, ok := args[0].(SearchField)
	if !ok {
		return nil, fmt.Errorf("first argument must hava a type of SearchField")
	}

	switch field {

	// Normal string fields
	case FieldAll, FieldRequestBody, FieldResponseBody, FieldAllBody, FieldWSMessage, FieldMethod, FieldHost, FieldPath, FieldStatusCode, FieldTag, FieldId:
		getter, err := CreateStrFieldGetter(field)
		if err != nil {
			return nil, fmt.Errorf("error performing search: %s", err.Error())
		}

		if len(args) != 3 {
			return nil, errors.New("searches through strings must have one checker and one value")
		}

		comparer, ok := args[1].(StrComparer)
		if !ok {
			return nil, errors.New("comparer must be a StrComparer")
		}

		return GenStrFieldChecker(getter, comparer, args[2])

	// Normal key/value fields
	case FieldRequestHeaders, FieldResponseHeaders, FieldBothHeaders, FieldBothParam, FieldURLParam, FieldPostParam, FieldResponseCookie, FieldRequestCookie, FieldBothCookie:
		getter, err := CreateKvPairGetter(field)
		if err != nil {
			return nil, fmt.Errorf("error performing search: %s", err.Error())
		}

		if len(args) == 3 {
			// Get comparer and value out of function arguments
			comparer, ok := args[1].(StrComparer)
			if !ok {
				return nil, errors.New("comparer must be a StrComparer")
			}

			// Create a StrFieldGetter out of our key/value getter
			strgetter := func(req *ProxyRequest) ([]string, error) {
				pairs, err := getter(req)
				if err != nil {
					return nil, err
				}
				return pairsToStrings(pairs), nil
			}

			// return a str field checker using our new str getter
			return GenStrFieldChecker(strgetter, comparer, args[2])
		} else if len(args) == 5 {
			// Get comparer and value out of function arguments
			comparer1, ok := args[1].(StrComparer)
			if !ok {
				return nil, errors.New("first comparer must be a StrComparer")
			}

			val1, ok := args[2].(string)
			if !ok {
				return nil, errors.New("first val must be a list of bytes")
			}

			comparer2, ok := args[3].(StrComparer)
			if !ok {
				return nil, errors.New("second comparer must be a StrComparer")
			}

			val2, ok := args[4].(string)
			if !ok {
				return nil, errors.New("second val must be a list of bytes")
			}

			// Create a checker out of our getter, comparers, and vals
			return GenKvFieldChecker(getter, comparer1, val1, comparer2, val2)
		} else {
			return nil, errors.New("invalid number of arguments for a key/value search")
		}

	// Other fields
	case FieldAfter:
		if len(args) != 2 {
			return nil, errors.New("searching by 'after' takes exactly on parameter")
		}

		val, ok := args[1].(time.Time)
		if !ok {
			return nil, errors.New("search argument must be a time.Time")
		}

		return func(req *ProxyRequest) bool {
			return req.StartDatetime.After(val)
		}, nil

	case FieldBefore:
		if len(args) != 2 {
			return nil, errors.New("searching by 'before' takes exactly one parameter")
		}

		val, ok := args[1].(time.Time)
		if !ok {
			return nil, errors.New("search argument must be a time.Time")
		}

		return func(req *ProxyRequest) bool {
			return req.StartDatetime.Before(val)
		}, nil

	case FieldTimeRange:
		if len(args) != 3 {
			return nil, errors.New("searching by time range takes exactly two parameters")
		}

		begin, ok := args[1].(time.Time)
		if !ok {
			return nil, errors.New("search arguments must be a time.Time")
		}

		end, ok := args[2].(time.Time)
		if !ok {
			return nil, errors.New("search arguments must be a time.Time")
		}

		return func(req *ProxyRequest) bool {
			return req.StartDatetime.After(begin) && req.StartDatetime.Before(end)
		}, nil

	case FieldInvert:
		orig, err := NewRequestChecker(args[1:]...)
		if err != nil {
			return nil, fmt.Errorf("error with query to invert: %s", err.Error())
		}
		return func(req *ProxyRequest) bool {
			return !orig(req)
		}, nil

	default:
		return nil, errors.New("invalid field")
	}
}

func CreateStrFieldGetter(field SearchField) (StrFieldGetter, error) {
	// Returns a function to pull the relevant strings out of the request

	switch field {
	case FieldAll:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			strs = append(strs, string(req.FullMessage()))

			if req.ServerResponse != nil {
				strs = append(strs, string(req.ServerResponse.FullMessage()))
			}

			for _, wsm := range req.WSMessages {
				strs = append(strs, string(wsm.Message))
			}

			return strs, nil
		}, nil
	case FieldRequestBody:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			strs = append(strs, string(req.BodyBytes()))
			return strs, nil
		}, nil
	case FieldResponseBody:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			if req.ServerResponse != nil {
				strs = append(strs, string(req.ServerResponse.BodyBytes()))
			}
			return strs, nil
		}, nil
	case FieldAllBody:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			strs = append(strs, string(req.BodyBytes()))
			if req.ServerResponse != nil {
				strs = append(strs, string(req.ServerResponse.BodyBytes()))
			}
			return strs, nil
		}, nil
	case FieldWSMessage:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)

			for _, wsm := range req.WSMessages {
				strs = append(strs, string(wsm.Message))
			}

			return strs, nil
		}, nil
	case FieldMethod:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			strs = append(strs, req.Method)
			return strs, nil
		}, nil
	case FieldHost:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			strs = append(strs, req.DestHost)
			strs = append(strs, req.Host)
			return strs, nil
		}, nil
	case FieldPath:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			strs = append(strs, req.URL.Path)
			return strs, nil
		}, nil
	case FieldURL:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			strs = append(strs, req.FullURL().String())
			return strs, nil
		}, nil
	case FieldStatusCode:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 0)
			if req.ServerResponse != nil {
				strs = append(strs, strconv.Itoa(req.ServerResponse.StatusCode))
			}
			return strs, nil
		}, nil
	case FieldId:
		return func(req *ProxyRequest) ([]string, error) {
			strs := make([]string, 1)
			strs[0] = req.DbId
			return strs, nil
		}, nil
	default:
		return nil, errors.New("field is not a string")
	}
}

func GenStrChecker(cmp StrComparer, argval interface{}) (func(str string) bool, error) {
	// Create a function to check if a string matches a value using the given comparer
	switch cmp {
	case StrContains:
		val, ok := argval.(string)
		if !ok {
			return nil, errors.New("argument must be a string")
		}
		return func(str string) bool {
			if strings.Contains(str, val) {
				return true
			}
			return false
		}, nil
	case StrIs:
		val, ok := argval.(string)
		if !ok {
			return nil, errors.New("argument must be a string")
		}
		return func(str string) bool {
			if str == val {
				return true
			}
			return false
		}, nil
	case StrContainsRegexp:
		val, ok := argval.(string)
		if !ok {
			return nil, errors.New("argument must be a string")
		}
		regex, err := regexp.Compile(string(val))
		if err != nil {
			return nil, fmt.Errorf("could not compile regular expression: %s", err.Error())
		}
		return func(str string) bool {
			return regex.MatchString(string(str))
		}, nil
	case StrLengthGreaterThan:
		val, ok := argval.(int)
		if !ok {
			return nil, errors.New("argument must be an integer")
		}
		return func(str string) bool {
			if len(str) > val {
				return true
			}
			return false
		}, nil
	case StrLengthLessThan:
		val, ok := argval.(int)
		if !ok {
			return nil, errors.New("argument must be an integer")
		}
		return func(str string) bool {
			if len(str) < val {
				return true
			}
			return false
		}, nil
	case StrLengthEqualTo:
		val, ok := argval.(int)
		if !ok {
			return nil, errors.New("argument must be an integer")
		}
		return func(str string) bool {
			if len(str) == val {
				return true
			}
			return false
		}, nil
	default:
		return nil, errors.New("invalid comparer")
	}
}

func GenStrFieldChecker(strGetter StrFieldGetter, cmp StrComparer, val interface{}) (RequestChecker, error) {
	// Generates a request checker from a string getter, a comparer, and a value
	getter := strGetter
	comparer, err := GenStrChecker(cmp, val)
	if err != nil {
		return nil, err
	}

	return func(req *ProxyRequest) bool {
		strs, err := getter(req)
		if err != nil {
			panic(err)
		}
		for _, str := range strs {
			if comparer(str) {
				return true
			}
		}
		return false
	}, nil
}

func pairValuesFromHeader(header http.Header) []*PairValue {
	// Returns a list of pair values from a http.Header
	pairs := make([]*PairValue, 0)
	for k, vs := range header {
		for _, v := range vs {
			pair := &PairValue{string(k), string(v)}
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

func pairValuesFromURLQuery(values url.Values) []*PairValue {
	// Returns a list of pair values from a http.Header
	pairs := make([]*PairValue, 0)
	for k, vs := range values {
		for _, v := range vs {
			pair := &PairValue{string(k), string(v)}
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

func pairValuesFromCookies(cookies []*http.Cookie) []*PairValue {
	pairs := make([]*PairValue, 0)
	for _, c := range cookies {
		pair := &PairValue{string(c.Name), string(c.Value)}
		pairs = append(pairs, pair)
	}
	return pairs
}

func pairsToStrings(pairs []*PairValue) []string {
	// Converts a list of pairs into a list of strings containing all keys and values
	// k1: v1, k2: v2 -> ["k1", "v1", "k2", "v2"]
	strs := make([]string, 0)
	for _, p := range pairs {
		strs = append(strs, p.key)
		strs = append(strs, p.value)
	}
	return strs
}

func CreateKvPairGetter(field SearchField) (KvFieldGetter, error) {
	// Returns a function to pull the relevant pairs out of the request
	switch field {
	case FieldRequestHeaders:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			return pairValuesFromHeader(req.Header), nil
		}, nil
	case FieldResponseHeaders:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			var pairs []*PairValue
			if req.ServerResponse != nil {
				pairs = pairValuesFromHeader(req.ServerResponse.Header)
			} else {
				pairs = make([]*PairValue, 0)
			}
			return pairs, nil
		}, nil
	case FieldBothHeaders:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			pairs := pairValuesFromHeader(req.Header)
			if req.ServerResponse != nil {
				pairs = append(pairs, pairValuesFromHeader(req.ServerResponse.Header)...)
			}
			return pairs, nil
		}, nil
	case FieldBothParam:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			pairs := pairValuesFromURLQuery(req.URL.Query())
			params, err := req.PostParameters()
			if err == nil {
				pairs = append(pairs, pairValuesFromURLQuery(params)...)
			}
			return pairs, nil
		}, nil
	case FieldURLParam:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			return pairValuesFromURLQuery(req.URL.Query()), nil
		}, nil
	case FieldPostParam:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			params, err := req.PostParameters()
			if err != nil {
				return nil, err
			}
			return pairValuesFromURLQuery(params), nil
		}, nil
	case FieldResponseCookie:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			pairs := make([]*PairValue, 0)
			if req.ServerResponse != nil {
				cookies := req.ServerResponse.Cookies()
				pairs = append(pairs, pairValuesFromCookies(cookies)...)
			}
			return pairs, nil
		}, nil
	case FieldRequestCookie:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			return pairValuesFromCookies(req.Cookies()), nil
		}, nil
	case FieldBothCookie:
		return func(req *ProxyRequest) ([]*PairValue, error) {
			pairs := pairValuesFromCookies(req.Cookies())
			if req.ServerResponse != nil {
				cookies := req.ServerResponse.Cookies()
				pairs = append(pairs, pairValuesFromCookies(cookies)...)
			}
			return pairs, nil
		}, nil
	default:
		return nil, errors.New("not implemented")
	}
}

func GenKvFieldChecker(kvGetter KvFieldGetter, cmp1 StrComparer, val1 string,
	cmp2 StrComparer, val2 string) (RequestChecker, error) {
	getter := kvGetter
	cmpfunc1, err := GenStrChecker(cmp1, val1)
	if err != nil {
		return nil, err
	}

	cmpfunc2, err := GenStrChecker(cmp2, val2)
	if err != nil {
		return nil, err
	}

	return func(req *ProxyRequest) bool {
		pairs, err := getter(req)
		if err != nil {
			return false
		}

		for _, p := range pairs {
			if cmpfunc1(p.key) && cmpfunc2(p.value) {
				return true
			}
		}
		return false
	}, nil
}

func CheckerFromPhrase(phrase QueryPhrase) (RequestChecker, error) {
	checkers := make([]RequestChecker, len(phrase))
	for i, args := range phrase {
		newChecker, err := NewRequestChecker(args...)
		if err != nil {
			return nil, fmt.Errorf("error with search %d: %s", i, err.Error())
		}
		checkers[i] = newChecker
	}

	ret := func(req *ProxyRequest) bool {
		for _, checker := range checkers {
			if checker(req) {
				return true
			}
		}
		return false
	}

	return ret, nil
}

func CheckerFromMessageQuery(query MessageQuery) (RequestChecker, error) {
	checkers := make([]RequestChecker, len(query))
	for i, phrase := range query {
		newChecker, err := CheckerFromPhrase(phrase)
		if err != nil {
			return nil, fmt.Errorf("error with phrase %d: %s", i, err.Error())
		}
		checkers[i] = newChecker
	}

	ret := func(req *ProxyRequest) bool {
		for _, checker := range checkers {
			if !checker(req) {
				return false
			}
		}
		return true
	}

	return ret, nil
}

/*
StringSearch conversions
*/

func FieldGoToString(field SearchField) (string, error) {
	switch field {
	case FieldAll:
		return "all", nil
	case FieldRequestBody:
		return "reqbody", nil
	case FieldResponseBody:
		return "rspbody", nil
	case FieldAllBody:
		return "body", nil
	case FieldWSMessage:
		return "wsmessage", nil
	case FieldRequestHeaders:
		return "reqheader", nil
	case FieldResponseHeaders:
		return "rspheader", nil
	case FieldBothHeaders:
		return "header", nil
	case FieldMethod:
		return "method", nil
	case FieldHost:
		return "host", nil
	case FieldPath:
		return "path", nil
	case FieldURL:
		return "url", nil
	case FieldStatusCode:
		return "statuscode", nil
	case FieldBothParam:
		return "param", nil
	case FieldURLParam:
		return "urlparam", nil
	case FieldPostParam:
		return "postparam", nil
	case FieldResponseCookie:
		return "rspcookie", nil
	case FieldRequestCookie:
		return "reqcookie", nil
	case FieldBothCookie:
		return "cookie", nil
	case FieldTag:
		return "tag", nil
	case FieldAfter:
		return "after", nil
	case FieldBefore:
		return "before", nil
	case FieldTimeRange:
		return "timerange", nil
	case FieldInvert:
		return "invert", nil
	case FieldId:
		return "dbid", nil
	default:
		return "", errors.New("invalid field")
	}
}

func FieldStrToGo(field string) (SearchField, error) {
	switch strings.ToLower(field) {
	case "all":
		return FieldAll, nil
	case "reqbody", "reqbd", "qbd", "qdata", "qdt":
		return FieldRequestBody, nil
	case "rspbody", "rspbd", "sbd", "sdata", "sdt":
		return FieldResponseBody, nil
	case "body", "bd", "data", "dt":
		return FieldAllBody, nil
	case "wsmessage", "wsm":
		return FieldWSMessage, nil
	case "reqheader", "reqhd", "qhd":
		return FieldRequestHeaders, nil
	case "rspheader", "rsphd", "shd":
		return FieldResponseHeaders, nil
	case "header", "hd":
		return FieldBothHeaders, nil
	case "method", "verb", "vb":
		return FieldMethod, nil
	case "host", "domain", "hs", "dm":
		return FieldHost, nil
	case "path", "pt":
		return FieldPath, nil
	case "url":
		return FieldURL, nil
	case "statuscode", "sc":
		return FieldStatusCode, nil
	case "param", "pm":
		return FieldBothParam, nil
	case "urlparam", "uparam":
		return FieldURLParam, nil
	case "postparam", "pparam":
		return FieldPostParam, nil
	case "rspcookie", "rspck", "sck":
		return FieldResponseCookie, nil
	case "reqcookie", "reqck", "qck":
		return FieldRequestCookie, nil
	case "cookie", "ck":
		return FieldBothCookie, nil
	case "tag":
		return FieldTag, nil
	case "after", "af":
		return FieldAfter, nil
	case "before", "b4":
		return FieldBefore, nil
	case "timerange":
		return FieldTimeRange, nil
	case "invert", "inv":
		return FieldInvert, nil
	case "dbid":
		return FieldId, nil
	default:
		return 0, fmt.Errorf("invalid field: %s", field)
	}
}

func CmpValGoToStr(comparer StrComparer, val interface{}) (string, string, error) {
	var cmpStr string
	switch comparer {
	case StrIs:
		cmpStr = "is"
		val, ok := val.(string)
		if !ok {
			return "", "", errors.New("val must be a string")
		}
		return cmpStr, val, nil
	case StrContains:
		cmpStr = "contains"
		val, ok := val.(string)
		if !ok {
			return "", "", errors.New("val must be a string")
		}
		return cmpStr, val, nil
	case StrContainsRegexp:
		cmpStr = "containsregexp"
		val, ok := val.(string)
		if !ok {
			return "", "", errors.New("val must be a string")
		}
		return cmpStr, val, nil
	case StrLengthGreaterThan:
		cmpStr = "lengt"
		val, ok := val.(int)
		if !ok {
			return "", "", errors.New("val must be an int")
		}
		return cmpStr, strconv.Itoa(val), nil
	case StrLengthLessThan:
		cmpStr = "lenlt"
		val, ok := val.(int)
		if !ok {
			return "", "", errors.New("val must be an int")
		}
		return cmpStr, strconv.Itoa(val), nil
	case StrLengthEqualTo:
		cmpStr = "leneq"
		val, ok := val.(int)
		if !ok {
			return "", "", errors.New("val must be an int")
		}
		return cmpStr, strconv.Itoa(val), nil
	default:
		return "", "", errors.New("invalid comparer")
	}
}

func CmpValStrToGo(strArgs []string) (StrComparer, interface{}, error) {
	if len(strArgs) != 2 {
		return 0, "", fmt.Errorf("parsing a comparer/val requires one comparer and one value. Got %d arguments.", len(strArgs))
	}

	switch strArgs[0] {
	case "is":
		return StrIs, strArgs[1], nil
	case "contains", "ct":
		return StrContains, strArgs[1], nil
	case "containsregexp", "ctr":
		return StrContainsRegexp, strArgs[1], nil
	case "lengt":
		i, err := strconv.Atoi(strArgs[1])
		if err != nil {
			return 0, nil, err
		}
		return StrLengthGreaterThan, i, nil
	case "lenlt":
		i, err := strconv.Atoi(strArgs[1])
		if err != nil {
			return 0, nil, err
		}
		return StrLengthLessThan, i, nil
	case "leneq":
		i, err := strconv.Atoi(strArgs[1])
		if err != nil {
			return 0, nil, err
		}
		return StrLengthEqualTo, i, nil
	default:
		return 0, "", fmt.Errorf("invalid comparer: %s", strArgs[0])
	}
}

func CheckArgsStrToGo(strArgs []string) ([]interface{}, error) {
	args := make([]interface{}, 0)
	if len(strArgs) == 0 {
		return nil, errors.New("missing field")
	}

	// Parse the field
	field, err := FieldStrToGo(strArgs[0])
	if err != nil {
		return nil, err
	}
	args = append(args, field)

	remaining := strArgs[1:]
	// Parse the query arguments
	switch args[0] {
	// Normal string fields
	case FieldAll, FieldRequestBody, FieldResponseBody, FieldAllBody, FieldWSMessage, FieldMethod, FieldHost, FieldPath, FieldStatusCode, FieldTag, FieldId:
		if len(remaining) != 2 {
			return nil, errors.New("string field searches require one comparer and one value")
		}

		cmp, val, err := CmpValStrToGo(remaining)
		if err != nil {
			return nil, err
		}
		args = append(args, cmp)
		args = append(args, val)
	// Normal key/value fields
	case FieldRequestHeaders, FieldResponseHeaders, FieldBothHeaders, FieldBothParam, FieldURLParam, FieldPostParam, FieldResponseCookie, FieldRequestCookie, FieldBothCookie:
		if len(remaining) == 2 {
			cmp, val, err := CmpValStrToGo(remaining)
			if err != nil {
				return nil, err
			}
			args = append(args, cmp)
			args = append(args, val)
		} else if len(remaining) == 4 {
			cmp, val, err := CmpValStrToGo(remaining[0:2])
			if err != nil {
				return nil, err
			}
			args = append(args, cmp)
			args = append(args, val)

			cmp, val, err = CmpValStrToGo(remaining[2:4])
			if err != nil {
				return nil, err
			}
			args = append(args, cmp)
			args = append(args, val)
		} else {
			return nil, errors.New("key/value field searches require either one comparer and one value or two comparer/value pairs")
		}

	// Other fields
	case FieldAfter, FieldBefore:
		if len(remaining) != 1 {
			return nil, errors.New("before/after take exactly one argument")
		}
		nanoseconds, err := strconv.ParseInt(remaining[0], 10, 64)
		if err != nil {
			return nil, errors.New("error parsing time")
		}
		timeVal := time.Unix(0, nanoseconds)
		args = append(args, timeVal)
	case FieldTimeRange:
		if len(remaining) != 2 {
			return nil, errors.New("time range takes exactly two arguments")
		}
		startNanoseconds, err := strconv.ParseInt(remaining[0], 10, 64)
		if err != nil {
			return nil, errors.New("error parsing start time")
		}
		startTimeVal := time.Unix(0, startNanoseconds)
		args = append(args, startTimeVal)

		endNanoseconds, err := strconv.ParseInt(remaining[1], 10, 64)
		if err != nil {
			return nil, errors.New("error parsing end time")
		}
		endTimeVal := time.Unix(0, endNanoseconds)
		args = append(args, endTimeVal)
	case FieldInvert:
		remainingArgs, err := CheckArgsStrToGo(remaining)
		if err != nil {
			return nil, fmt.Errorf("error with query to invert: %s", err.Error())
		}
		args = append(args, remainingArgs...)
	default:
		return nil, fmt.Errorf("field not yet implemented: %s", strArgs[0])
	}

	return args, nil
}

func CheckArgsGoToStr(args []interface{}) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New("no arguments")
	}

	retargs := make([]string, 0)

	field, ok := args[0].(SearchField)
	if !ok {
		return nil, errors.New("first argument is not a field")
	}

	strField, err := FieldGoToString(field)
	if err != nil {
		return nil, err
	}
	retargs = append(retargs, strField)

	switch field {
	case FieldAll, FieldRequestBody, FieldResponseBody, FieldAllBody, FieldWSMessage, FieldMethod, FieldHost, FieldPath, FieldStatusCode, FieldTag, FieldId:
		if len(args) != 3 {
			return nil, errors.New("string fields require exactly two arguments")
		}

		comparer, ok := args[1].(StrComparer)
		if !ok {
			return nil, errors.New("comparer must be a StrComparer")
		}

		cmpStr, valStr, err := CmpValGoToStr(comparer, args[2])
		if err != nil {
			return nil, err
		}
		retargs = append(retargs, cmpStr)
		retargs = append(retargs, valStr)
		return retargs, nil

	case FieldRequestHeaders, FieldResponseHeaders, FieldBothHeaders, FieldBothParam, FieldURLParam, FieldPostParam, FieldResponseCookie, FieldRequestCookie, FieldBothCookie:
		if len(args) == 3 {
			comparer, ok := args[1].(StrComparer)
			if !ok {
				return nil, errors.New("comparer must be a StrComparer")
			}

			cmpStr, valStr, err := CmpValGoToStr(comparer, args[2])
			if err != nil {
				return nil, err
			}
			retargs = append(retargs, cmpStr)
			retargs = append(retargs, valStr)

			return retargs, nil
		} else if len(args) == 5 {
			comparer1, ok := args[1].(StrComparer)
			if !ok {
				return nil, errors.New("comparer1 must be a StrComparer")
			}

			cmpStr1, valStr1, err := CmpValGoToStr(comparer1, args[2])
			if err != nil {
				return nil, err
			}
			retargs = append(retargs, cmpStr1)
			retargs = append(retargs, valStr1)

			comparer2, ok := args[1].(StrComparer)
			if !ok {
				return nil, errors.New("comparer2 must be a StrComparer")
			}

			cmpStr2, valStr2, err := CmpValGoToStr(comparer2, args[2])
			if err != nil {
				return nil, err
			}
			retargs = append(retargs, cmpStr2)
			retargs = append(retargs, valStr2)

			return retargs, nil
		} else {
			return nil, errors.New("key/value queries take exactly two or four arguments")
		}

	case FieldAfter, FieldBefore:
		if len(args) != 2 {
			return nil, errors.New("before/after fields require exactly one argument")
		}

		time, ok := args[1].(time.Time)
		if !ok {
			return nil, errors.New("argument must have a type of time.Time")
		}
		nanoseconds := time.UnixNano()
		retargs = append(retargs, strconv.FormatInt(nanoseconds, 10))
		return retargs, nil

	case FieldTimeRange:
		if len(args) != 3 {
			return nil, errors.New("time range fields require exactly two arguments")
		}

		time1, ok := args[1].(time.Time)
		if !ok {
			return nil, errors.New("arguments must have a type of time.Time")
		}
		nanoseconds1 := time1.UnixNano()
		retargs = append(retargs, strconv.FormatInt(nanoseconds1, 10))

		time2, ok := args[2].(time.Time)
		if !ok {
			return nil, errors.New("arguments must have a type of time.Time")
		}
		nanoseconds2 := time2.UnixNano()
		retargs = append(retargs, strconv.FormatInt(nanoseconds2, 10))
		return retargs, nil

	case FieldInvert:
		strs, err := CheckArgsGoToStr(args[1:])
		if err != nil {
			return nil, err
		}
		retargs = append(retargs, strs...)
		return retargs, nil

	default:
		return nil, fmt.Errorf("invalid field")
	}
}

func StrPhraseToGoPhrase(phrase StrQueryPhrase) (QueryPhrase, error) {
	goPhrase := make(QueryPhrase, len(phrase))
	for i, strArgs := range phrase {
		var err error
		goPhrase[i], err = CheckArgsStrToGo(strArgs)
		if err != nil {
			return nil, fmt.Errorf("Error with argument set %d: %s", i, err.Error())
		}
	}
	return goPhrase, nil
}

func GoPhraseToStrPhrase(phrase QueryPhrase) (StrQueryPhrase, error) {
	strPhrase := make(StrQueryPhrase, len(phrase))
	for i, goArgs := range phrase {
		var err error
		strPhrase[i], err = CheckArgsGoToStr(goArgs)
		if err != nil {
			return nil, fmt.Errorf("Error with argument set %d: %s", i, err.Error())
		}
	}
	return strPhrase, nil
}

func StrQueryToGoQuery(query StrMessageQuery) (MessageQuery, error) {
	goQuery := make(MessageQuery, len(query))
	for i, phrase := range query {
		var err error
		goQuery[i], err = StrPhraseToGoPhrase(phrase)
		if err != nil {
			return nil, fmt.Errorf("Error with phrase %d: %s", i, err.Error())
		}
	}
	return goQuery, nil
}

func GoQueryToStrQuery(query MessageQuery) (StrMessageQuery, error) {
	strQuery := make(StrMessageQuery, len(query))
	for i, phrase := range query {
		var err error
		strQuery[i], err = GoPhraseToStrPhrase(phrase)
		if err != nil {
			return nil, fmt.Errorf("Error with phrase %d: %s", i, err.Error())
		}
	}
	return strQuery, nil
}
