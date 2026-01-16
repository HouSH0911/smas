#!/bin/sh
PRO_PATH="/home/haohan/tmas/bin"
PROGRAM="tmas"
WATCH_PROGRAM='tmas_start.sh'
COUNT=0

function rand()
{
    min=$1
    max=$(($2-$min+1))
    num=$(cat /proc/sys/kernel/random/uuid | cksum | awk -F ' ' '{print $1}')
    echo $(($num%$max+$min)) 
}

# Check and create logrotate config if not exists
LOGROTATE_CONF="/etc/logrotate.d/smas"
if [ ! -f "$LOGROTATE_CONF" ]; then
    cat << EOF > "$LOGROTATE_CONF"
/home/haohan/smas/log/smas_console.log {
    daily
    rotate 30
    missingok
    dateext
    notifempty
    copytruncate
}
EOF
    if [ $? -eq 0 ]; then
        echo "Logrotate config created at $LOGROTATE_CONF"
    else
        echo "Failed to create logrotate config (may require sudo)"
    fi
fi

pid=`echo $$`
PROCESS=`ps -ef |grep -w $WATCH_PROGRAM | grep -v $pid| grep -v grep | wc -l`
if [ $PROCESS -gt 0 ]; then
    rnd=$(rand 1000000 5000000) 
    usleep $rnd
    PROCESS_AGAIN=`ps -ef |grep -w $WATCH_PROGRAM | grep -v $pid| grep -v grep | wc -l`
    if [ $PROCESS_AGAIN -gt 0 ]; then
        echo "$WATCH_PROGRAM is running, current $WATCH_PROGRAM will exit!!!"
        exit 0
    fi
fi
# 死循环持续监测进程是否在运行
while true ; do
    PRO_NOW=`pidof "$PROGRAM" | wc -l`
    if [ $PRO_NOW -lt 1 ]; then
        PRO_NOW_AGAIN=`pidof "$PROGRAM" | wc -l`
        if [ $PRO_NOW_AGAIN -lt 1 ]; then
            cd $PRO_PATH
            nohup stdbuf -oL ./$PROGRAM >> $PRO_PATH/../log/tmas_console.log 2>&1 &
        fi
    fi
sleep 2
done
exit 0
