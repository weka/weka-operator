pdsh -w "wekabox[14-18].lan" "weka local ps | grep Running| awk '{print \$1}' | xargs -n1 -iNN weka local stop"
pdsh -w "wekabox[14-18].lan" "weka local ps | grep Stopped| awk '{print \$1}' | xargs -n1 weka local rm --force"
pdsh -w "wekabox[14-18].lan" rm -rf /opt/weka/compute /opt/weka/backend\* /opt/weka/drive
