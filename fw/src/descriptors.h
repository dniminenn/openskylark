#ifndef DESCRIPTORS_H
#define DESCRIPTORS_H

#include <stdint.h>

extern __code const uint8_t desc_device_uac1[18];
extern __code const uint8_t desc_device_uac2[18];
extern __code const uint8_t desc_langid[4];
extern __code const uint8_t desc_hid_report[43];
extern __code const uint8_t desc_hid[9];
extern __code const uint8_t desc_qualifier[10];
extern __code const uint8_t desc_config_uac1[159];
extern __code const uint8_t desc_config_uac2[311];
extern __code const uint8_t desc_config_other_speed[190];
extern __code const uint8_t desc_update_device[18];
extern __code const uint8_t desc_update_config[18];

#endif
