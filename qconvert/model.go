package qconvert

import "encoding/json"

// ToModel
//
//	@Description: 将任意类型转为指定类型，此方法如果发生会抛出
//	@param raw 原始对象
//	@return T 指定类型
func ToModel[T any](raw any) T {
	if raw == nil {
		return *new(T)
	}
	js, err := json.Marshal(raw)
	if err != nil {
		panic(err)
	}
	dbModel := new(T)
	err = json.Unmarshal(js, &dbModel)
	if err != nil {
		panic(err)
	}
	return *dbModel
}
