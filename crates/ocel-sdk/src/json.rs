use buffa_types::google::protobuf::{Struct, Timestamp, Value};
use std::time::SystemTime;

const LARGEST_EXACT_INTEGER: f64 = 9_007_199_254_740_992.0;

pub(crate) fn convert_value(value: Option<&Value>) -> serde_json::Value {
    let Some(value) = value else {
        return serde_json::Value::Null;
    };
    if let Some(number) = value.as_number() {
        return convert_number(number);
    }
    if let Some(text) = value.as_str() {
        return serde_json::Value::String(text.to_string());
    }
    if let Some(flag) = value.as_bool() {
        return serde_json::Value::Bool(flag);
    }
    if let Some(object) = value.as_struct() {
        return serde_json::Value::Object(convert_object(object));
    }
    if let Some(list) = value.as_list() {
        return serde_json::Value::Array(list.iter().map(|one| convert_value(Some(one))).collect());
    }
    serde_json::Value::Null
}

pub(crate) fn convert_object(object: &Struct) -> serde_json::Map<String, serde_json::Value> {
    object
        .fields
        .iter()
        .map(|(name, value)| (name.clone(), convert_value(Some(value))))
        .collect()
}

fn convert_number(number: f64) -> serde_json::Value {
    if number.fract() == 0.0 && number.abs() < LARGEST_EXACT_INTEGER {
        return serde_json::Value::from(number as i64);
    }
    serde_json::Number::from_f64(number)
        .map(serde_json::Value::Number)
        .unwrap_or(serde_json::Value::Null)
}

pub(crate) fn convert_timestamp(at: &Timestamp) -> Option<SystemTime> {
    SystemTime::try_from(at.clone()).ok()
}
