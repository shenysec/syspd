## 描述
根据金融行业监管要求写的一个失效链接排查扫描器！

## 最近更新
~~~
1、更换为flag进行参数解析
2、添加失效链接来源标记
3、处理所有4xx、5xx响应情况
4、IP封禁检测提醒
~~~
## 使用方法
~~~ 
--help

  -c string
        Cookie for authentication
  -d int
        Depth of processing (default 3)
  -f int
        Function to execute (1 for default -失效链接) (default 1)
  -p int
        Number of concurrent processes (default 3)
  -u string
        URL to process
~~~
## 注意事项
API 扫描功能需自行改一下才能用，改了不少，懒得改回去了。
